package main

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xi2/xz"
)

// the archive that this source code can be found within.  the root of the site
// redirects to README.md within this archive.
const thisArchive = "https://dezip.org/dezip-1.0.zip"

// how long can the URL's path be before it's rejected?
const URLPathLimit = 999

// the maximum number of components allowed in a URL.
const URLComponentLimit = 99

// analogous limits for the path within the archive.  255 is important here
// because it keeps the lengths small enough to fit into one byte in the search
// index.
const archivePathLimit = 255
const archiveComponentLimit = 99

// files above this limit aren't rendered.
const textFileSizeLimit = 10_000_000 // 10 MB

// archives with a directory having more than this number of entries will fail
// to render.
const entriesPerDirectoryLimit = 99999

// downloads over this limit will fail.
const archiveSizeLimit = 100_000_000 // 100 MB

// unpacking an archive larger than this limit will fail.
const uncompressedArchiveSizeLimit = 300_000_000 // 300 MB

// the maximum number of "weird" characters above 0xF4 that can appear before a
// file is considered a binary file.
const weirdCharacterLimit = 3

// the number of renderer goroutines running at once.
const numberOfRenderers = 2

// the number of archives that can be in a state other than finished or failed.
const activeArchiveLimit = 4

// the name of the "index.html" file.  dezip will ignore files and directories
// in the archive if they have this name.
const indexFileName = "hidden.from.dezip.html"

// how many seconds before a search times out?
const searchTimeoutSeconds = 20

// the maximum number of search results (to avoid overloading browsers).
const searchResultLimit = 9999

// a singleton closed channel that's initialized in main().
var closedChannel chan struct{}

// the regexp [^a-zA-Z0-9].
var alphanum *regexp.Regexp

// which protocols can you use to fetch archives?  and which formats can those
// archives be in?  protocols are keyed by url scheme, and formats are keyed by
// file extension.
var protocols map[string]protocol
var formats map[string]archiveFormat

// protects the following global variables.
var globalMutex sync.Mutex

// the number of archives in a non-terminal state.  limited to
// activeArchiveLimit.
var activeArchives int

type cache struct {
	mutex sync.Mutex

	rootPath string
	metaPath string
	textPath string

	archivesByURL map[string]*archive
	// archives in the order they will be reclaimed (at the time of writing,
	// this is in creation order).
	archiveURLsToReclaim []string
	renderingArchiveURLs []string
	renderingCond        *sync.Cond
}

func main() {
	var err error
	alphanum, err = regexp.Compile("[^a-zA-Z0-9]")
	if err != nil {
		log.Fatal(err)
	}
	protocols = map[string]protocol{
		"http":  httpProtocol{},
		"https": httpProtocol{},
	}
	formats = map[string]archiveFormat{
		".zip": zipArchiveFormat{},

		".tgz":    tgzArchiveFormat{},
		".tar.gz": tgzArchiveFormat{},

		".tb2":     tbz2ArchiveFormat{},
		".tbz":     tbz2ArchiveFormat{},
		".tbz2":    tbz2ArchiveFormat{},
		".tar.bz2": tbz2ArchiveFormat{},

		".txz":    txzArchiveFormat{},
		".tar.xz": txzArchiveFormat{},
	}
	closedChannel = make(chan struct{})
	close(closedChannel)

	workingDirectory, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	c := &cache{
		rootPath: path.Join(workingDirectory, "root"),
		textPath: path.Join(workingDirectory, "text"),
		metaPath: path.Join(workingDirectory, "meta"),
	}
	c.renderingCond = sync.NewCond(&c.mutex)

	// decode existing archive metadata.
	c.archivesByURL, err = loadArchivesFromMetadata(c.metaPath)
	if err != nil {
		log.Fatal(err)
	}
	// sort by creation time to put the archives in reclamation order.
	var a []struct {
		url  string
		time time.Time
	}
	for k, v := range c.archivesByURL {
		a = append(a, struct {
			url  string
			time time.Time
		}{k, v.creationTime})
	}
	sort.Slice(a, func(i, j int) bool {
		return a[i].time.Before(a[j].time)
	})
	for _, v := range a {
		c.archiveURLsToReclaim = append(c.archiveURLsToReclaim, v.url)
	}

	// start the renderer goroutines.
	for i := 0; i < numberOfRenderers; i++ {
		go newRenderer().renderLoop(c)
	}

	// start the reclamation goroutine.
	go c.reclaimLoop()

	go func() {
		// on exit, reclaim all unfinished archives.
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		c.mutex.Lock()
		for url, ar := range c.archivesByURL {
			c.mutex.Unlock()
			ar.mutex.Lock()
			if ar.state != archiveStateFinished {
				ar.mutex.Unlock()
				c.reclaim(url)
			}
			c.mutex.Lock()
		}
		os.Exit(0)
	}()
	// start the web server.
	log.Fatal(http.ListenAndServe("127.0.0.1:8001", c))
}

func (c *cache) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if len(request.URL.Path) > URLPathLimit {
		response.WriteHeader(404)
		fmt.Fprint(response, "404 invalid url")
		return
	}
	if request.URL.Path == "/" {
		response.Header().Add("Location", rewriteURLv1(strings.Split("/"+thisArchive, "/"))+"dezip/README.md")
		response.WriteHeader(302)
		return
	}
	components := strings.SplitN(request.URL.Path, "/", URLComponentLimit+1)
	if len(components) == 2 {
		// this is so things still work without the usual reverse proxy deployment.
		http.ServeFile(response, request, path.Join(c.rootPath, request.URL.Path))
		return
	}
	if len(components) > URLComponentLimit || len(components) < 2 || components[0] != "" {
		response.WriteHeader(404)
		fmt.Fprint(response, "404 invalid url")
		return
	}
	rewrittenPath := ""
	if len(components) >= 3 && strings.HasSuffix(components[1], ":") {
		rewrittenPath = rewriteURLv1(components)
		components = strings.Split(rewrittenPath, "/")
	}
	if components[1] != "v1" {
		// plan ahead with a version number.
		response.WriteHeader(404)
		fmt.Fprint(response, "404 unrecognized version")
	} else if len(components) < 3 {
		response.WriteHeader(404)
		fmt.Fprint(response, "404 no path length specified")
	} else {
		// components[2] counts the components in the path to the archive root.
		// the rest are the path to the file or directory within the archive.
		length, err := strconv.Atoi(components[2])
		if err != nil || length < 4 || length >= len(components) {
			response.WriteHeader(404)
			fmt.Fprint(response, "404 invalid path length")
			return
		}
		var p page
		archiveComponents := components[:length]
		pathComponents := components[length:]
		if len(pathComponents) > 0 && pathComponents[len(pathComponents)-1] == "" {
			p.isDirectory = true
			pathComponents = pathComponents[:len(pathComponents)-1]
		}
		p.name = strings.Join(pathComponents, "/")

		// look up the proper protocol based on the scheme.  (https, gopher...)
		scheme := archiveComponents[3]
		fetchComponents := archiveComponents[4:]
		protocol, ok := protocols[scheme]
		if !ok {
			response.WriteHeader(404)
			p.writeErrorPage(response, fmt.Errorf("i don't know how to download %s:// urls", scheme))
			return
		}

		// construct the url which locates the archive.
		p.archiveURL = fmt.Sprintf("%s://%s", scheme, strings.Join(fetchComponents, "/"))

		if len(request.URL.Query()["remove"]) > 0 {
			response.Header().Set("Content-Type", "text/html;charset=utf-8")
			if request.Method == "POST" {
				c.mutex.Lock()
				archive := c.archivesByURL[p.archiveURL]
				c.mutex.Unlock()
				if archive != nil {
					archive.mutex.Lock()
					archive.transitionToState(archiveStateFailed)
					archive.failureReason = fmt.Errorf("archive removed using ?remove query parameter")
					archive.mutex.Unlock()
					if err := c.reclaim(p.archiveURL); err != nil {
						log.Print(err)
					}
					p.writeRemoveButtonPage(response, "archive removed.")
				} else {
					p.writeRemoveButtonPage(response, "archive not found.")
				}
			} else {
				p.writeRemoveButtonPage(response, "")
			}
			return
		}

		// check whether an archive downloaded (or currently downloading) from
		// this url is in the cache.  if not, create a new empty archive.
		c.mutex.Lock()
		archive := c.archivesByURL[p.archiveURL]
		if archive == nil {
			archive = newArchive(strings.Join(archiveComponents, "/"))
			if archive != nil {
				c.archivesByURL[p.archiveURL] = archive
				c.archiveURLsToReclaim = append(c.archiveURLsToReclaim, p.archiveURL)
			}
		}
		c.mutex.Unlock()

		if archive == nil {
			response.WriteHeader(503)
			p.writeErrorPage(response, fmt.Errorf("too many concurrent downloads; try again later"))
			return
		}

		archive.mutex.Lock()
		if archive.state == archiveStateInitial {
			var format archiveFormat
			for extension, fmt := range formats {
				if strings.HasSuffix(p.archiveURL, extension) {
					format = fmt
					break
				}
			}
			if format == nil {
				archive.transitionToState(archiveStateFailed)
				archive.failureReason = fmt.Errorf("based on its file extension, %v doesn't look like an archive file", fetchComponents[len(fetchComponents)-1])
			} else {
				// if the archive is newly initialized, start the download and
				// transition to the downloading state.
				archive.transitionToState(archiveStateDownloading)
				archive.progress.estimatedContentLength = archiveSizeLimit
				go func() {
					defer func() {
						if r := recover(); r != nil {
							log.Print("recovered in download: ", r, "\n", string(debug.Stack()))
							archive.mutex.Lock()
							archive.transitionToState(archiveStateFailed)
							archive.failureReason = fmt.Errorf("panic during download: %v\n%v", r, string(debug.Stack()))
							archive.mutex.Unlock()
						}
					}()
					if err := c.download(p, format, protocol, archive); err != nil {
						archive.mutex.Lock()
						archive.transitionToState(archiveStateFailed)
						archive.failureReason = err
						archive.mutex.Unlock()
					}
					archive.mutex.Lock()
					state := archive.state
					archive.mutex.Unlock()
					if state == archiveStateFailed {
						c.reclaimFiles(archive.path)
					}
				}()
			}
		}
		timeout := 1 * time.Second
		if len(request.Header["X-Dezip-Progress"]) > 0 {
			// deliver xhr progress updates immediately.
			timeout = 0
		} else if archive.state == archiveStateFailed {
			// there's no reason to wait if no further progress will be made.
			timeout = 0
		} else if archive.state == archiveStateRendering {
			// use a large timeout -- the file should be rendered right away
			// and the user wants to see it.  there's also no fallback page
			// like a progress bar in this case.
			timeout = 10 * time.Second
		}
		searchQuery := request.URL.Query()["search"]
		directorySearch := len(searchQuery) > 0 && p.isDirectory
		var ready chan struct{}
		if directorySearch {
			// directory searches require the entire archive to be downloaded.
			ready = archive.downloaded
		} else {
			// request that the file be rendered.  returns a channel that blocks
			// until the file is finished rendering.
			ready = archive.requestRender(p.name)
		}
		originalState := archive.state
		archive.mutex.Unlock()

		// wait a bit for the file to finish rendering.
		select {
		case <-ready:
			// either the file was rendered or the archive is in the finished
			// state (in this state the filesystem is the source of truth).
			// send the file, if it exists, as the response.
			if len(rewrittenPath) > 0 {
				// redirect to the /vN/... path which supports browsing within
				// the archive.
				archive.mutex.Lock()
				dir := archive.initialDirectory
				archive.mutex.Unlock()
				response.Header().Add("Location", path.Join(rewrittenPath, dir)+"/")
				response.WriteHeader(302)
				break
			}
			var filename string
			var info os.FileInfo
			if len(searchQuery) > 0 {
				filename = path.Join(c.textPath, request.URL.Path)
				info, err = os.Stat(filename)
			}
			if info == nil || err != nil || info.IsDir() {
				filename = path.Join(c.rootPath, request.URL.Path)
				info, err = os.Stat(filename)
			}
			var f *os.File
			if err == nil {
				if info.IsDir() {
					if !strings.HasSuffix(request.URL.Path, "/") {
						response.Header().Add("Location", request.URL.Path+"/")
						response.WriteHeader(302)
						break
					}
					f, err = os.Open(path.Join(filename, indexFileName))
				} else {
					f, err = os.Open(filename)
				}
			}
			if err != nil {
				// this is where archive 404s are reported.
				response.WriteHeader(404)
				fmt.Fprint(response, "404 not found")
				break
			}
			defer f.Close()
			response.Header().Set("Content-Type", "text/html;charset=utf-8")
			if directorySearch {
				filter := ""
				for _, v := range request.Cookies() {
					if v.Name == "filter" {
						filter, err = url.PathUnescape(v.Value)
						if err == nil {
							break
						}
					}
				}
				results := make(chan searchResult, 5)
				go c.search(archive, searchQuery[0], filter, results)
				p.writeSearchResultsPage(response, searchQuery[0], filter, results)
			} else if len(searchQuery) > 0 {
				insertSearchAnchors(response, f, searchQuery[0])
			} else {
				io.Copy(response, f)
			}
		case <-time.After(timeout):
			// the file wasn't rendered, even after the timeout.  what happens
			// next depends on the archive state.
			archive.mutex.Lock()
			state := archive.state
			reason := archive.failureReason
			progress := archive.progress
			archive.mutex.Unlock()
			switch state {
			case archiveStateDownloading:
				// while downloading, show a progress bar.
				p.writeProgressPage(response, progress)
			case archiveStateRendering:
				// there are two possibilities here:
				// - the timeout expired while waiting for a file to render.
				// - the state changed from downloading to rendering.
				// in the first case, show an error.  in the second case, have
				// the client retry the request by issuing a redirect.
				if originalState == archiveStateRendering {
					response.WriteHeader(503)
					fmt.Fprintln(response, "error 503")
					fmt.Fprintln(response, "this file is taking a long time to render.")
					fmt.Fprintln(response, "you can refresh if you want to keep waiting for it.")
				} else {
					response.Header().Add("Location", request.URL.Path)
					response.WriteHeader(302)
				}
			case archiveStateFinished:
				// the state changed to finished.  retry the request in case
				// the timeout expired just before the archive finished
				// rendering.
				response.Header().Add("Location", request.URL.Path)
				response.WriteHeader(302)
			case archiveStateFailed:
				response.WriteHeader(503)
				p.writeErrorPage(response, reason)
			default:
				response.WriteHeader(503)
				p.writeErrorPage(response, fmt.Errorf("unexpected archive state %v", state))
			}
		}
	}
}

func rewriteURLv1(components []string) string {
	// rewrite urls of the form dezip.org/scheme://....
	// make sure to handle the case where scheme://... becomes scheme:/... due
	// to path normalization.
	path := components[2:]
	if components[2] == "" && len(components) >= 4 {
		path = components[3:]
	}
	scheme := strings.ToLower(components[1])
	return fmt.Sprintf("/v1/%d/%s/%s/", len(path)+4, scheme[:len(scheme)-1], strings.Join(path, "/"))
}

// -- archive formats

type archiveFormat interface {
	download(*os.File, io.Reader) error
}

type zipArchiveFormat struct{}

func (a zipArchiveFormat) download(f *os.File, r io.Reader) error {
	_, err := io.Copy(f, r)
	return err
}

type tgzArchiveFormat struct{}

func (a tgzArchiveFormat) download(f *os.File, r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	return downloadTar(f, gz)
}

type tbz2ArchiveFormat struct{}

func (a tbz2ArchiveFormat) download(f *os.File, r io.Reader) error {
	return downloadTar(f, bzip2.NewReader(r))
}

type txzArchiveFormat struct{}

func (a txzArchiveFormat) download(f *os.File, r io.Reader) error {
	xz, err := xz.NewReader(r, xz.DefaultDictMax)
	if err != nil {
		return err
	}
	return downloadTar(f, xz)
}

func downloadTar(f *os.File, r io.Reader) error {
	tr := tar.NewReader(r)
	zw := zip.NewWriter(f)
	var totalSize int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		totalSize += hdr.Size
		if totalSize > uncompressedArchiveSizeLimit {
			return fmt.Errorf("uncompressed archive size exceeded limit of %d bytes", uncompressedArchiveSizeLimit)
		}
		mode := os.FileMode(hdr.Mode)
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeLink:
			mode |= os.ModeSymlink
		case tar.TypeSymlink:
			mode |= os.ModeSymlink
		case tar.TypeChar:
		case tar.TypeBlock:
		case tar.TypeDir:
		case tar.TypeFifo:
			// show these.
		default:
			// hide anything else (e.g. the pax_global_header that appears in
			// linux tarballs).
			continue
		}
		zhdr := &zip.FileHeader{
			Name:     hdr.Name,
			Method:   zip.Store,
			Modified: hdr.ModTime,
		}
		zhdr.SetMode(mode)
		if hdr.Typeflag == tar.TypeDir {
			if len(zhdr.Name) == 0 {
				continue
			} else if zhdr.Name[len(zhdr.Name)-1] != '/' {
				zhdr.Name += "/"
			}
		}
		w, err := zw.CreateHeader(zhdr)
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			io.WriteString(w, hdr.Linkname)
		} else {
			if _, err := io.Copy(w, tr); err != nil {
				return err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return nil
}

// -- protocols

type protocol interface {
	fetch(string) (response, error)
}
type response struct {
	body io.ReadCloser

	// may be negative, indicating an unknown content length.
	contentLength int64
}

type httpProtocol struct{}

func (p httpProtocol) fetch(url string) (response, error) {
	res, err := http.Get(url)
	if err != nil {
		return response{}, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return response{}, fmt.Errorf("%s when fetching %s", res.Status, url)
	}
	return response{res.Body, res.ContentLength}, nil
}
