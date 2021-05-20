package main

import (
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

type page struct {
	name        string
	isDirectory bool
	archiveURL  string
}

type contentType int

const (
	contentTypeText contentType = iota
	contentTypeMarkdown
)

func (p page) writeDirectoryPage(w io.Writer, dirs map[string]*archiveDirectory) {
	p.writePrologue(w)
	p.writeHeader(w, headerOptions{})
	fmt.Fprintln(w, "    <div class='directory-container'>")
	fmt.Fprintln(w, "      <table class='directory'>")
	fmt.Fprintln(w, "        <colgroup>")
	fmt.Fprintln(w, "          <col span='1'>")
	fmt.Fprintln(w, "          <col span='1' width='*'>")
	fmt.Fprintln(w, "          <col span='1' width='80px'>")
	fmt.Fprintln(w, "          <col span='1' class='size-column'>")
	fmt.Fprintln(w, "          <col span='1' class='mod-date-column'>")
	fmt.Fprintln(w, "        </colgroup>")
	dir := dirs[p.name]
	fmt.Fprint(w, "        <tr class='back'><td>&nbsp;</td><td class='filename' colspan='4'>")
	parentLinkClass := ""
	if len(dir.fileNames) > 0 && len(dir.directoryNames) == 0 {
		parentLinkClass = " class='adjust-for-dblborder'"
	}
	if len(p.name) > 0 {
		fmt.Fprint(w, "<a href='..'", parentLinkClass, ">..</a>")
	} else {
		fmt.Fprint(w, "<a", parentLinkClass, ">&nbsp;</a>")
	}
	fmt.Fprintln(w, "</td></tr>")
	if len(dir.fileNames)+len(dir.directoryNames) == 0 {
		fmt.Fprintln(w, "        <tr><td>&nbsp;</td><td colspan='4'><div class='empty'>empty directory</div></td></tr>")
	}
	for i, name := range dir.directoryNames {
		fmt.Fprint(w, "        <tr>")
		if i == 0 {
			fmt.Fprint(w, "<td class='category'>directories</td>")
		} else {
			fmt.Fprint(w, "<td>&nbsp;</td>")
		}
		entry := dir.entries[name]
		// look up the subdirectory entry to see how many files and directories
		// it contains.
		subdir := dirs[path.Join(p.name, name)]
		prefix := ""
		// while there's only a single subdirectory, add the intermediate
		// directory to a prefix and continue.
		for len(subdir.directoryNames) == 1 && len(subdir.fileNames) == 0 {
			prefix = path.Join(prefix, name)
			name = subdir.directoryNames[0]
			subdir = dirs[path.Join(p.name, prefix, name)]
		}
		fmt.Fprint(w, "<td class='filename'>")
		if len(prefix) > 0 {
			fmt.Fprintf(w, "<a href='./%s/%s/'><span class='prefix'>%s/</span>%s</a></td>", html.EscapeString(escapeURLPath(prefix)), html.EscapeString(escapeURLPath(name)), html.EscapeString(prefix), html.EscapeString(name))
		} else {
			fmt.Fprintf(w, "<a href='./%s/'>%s</a></td>", html.EscapeString(escapeURLPath(name)), html.EscapeString(name))
		}
		if len(subdir.fileNames) > 0 {
			fs := "file"
			if len(subdir.fileNames) != 1 {
				fs = "files"
			}
			fmt.Fprintf(w, "<td>%d %s</td>", len(subdir.fileNames), fs)
		} else {
			fmt.Fprint(w, "<td class='light'>&mdash;</td>")
		}
		if len(subdir.directoryNames) == 1 {
			fmt.Fprint(w, "<td>1 <span class='abbr abbr-subdirectory'><span>subdirectory</span></span></td>")
		} else if len(subdir.directoryNames) > 1 {
			fmt.Fprintf(w, "<td>%d <span class='abbr abbr-subdirectories'><span>subdirectories</span></span></td>", len(subdir.directoryNames))
		} else {
			fmt.Fprint(w, "<td class='light'>&mdash;</td>")
		}
		fmt.Fprintf(w, "<td>%s</td>", formatTime(entry.modified))
		fmt.Fprintln(w, "</tr>")
	}
	for i, name := range dir.fileNames {
		if i == 0 {
			fmt.Fprint(w, "        <tr class='dblborder'><td class='category'>files</td>")
		} else {
			fmt.Fprint(w, "        <tr><td>&nbsp;</td>")
		}
		entry := dir.entries[name]
		mode := entry.file.Mode()
		fmt.Fprint(w, "<td class='filename'>")
		if mode&os.ModeSymlink != 0 {
			fmt.Fprint(w, "<div>")
		}
		fmt.Fprintf(w, "<a href='./%s'>%s</a>", html.EscapeString(escapeURLPath(name)), html.EscapeString(name))
		if mode&os.ModeSymlink != 0 {
			fmt.Fprint(w, " &#x2192; ")
			rc, err := entry.file.Open()
			if err == nil {
				bytes, err := ioutil.ReadAll(rc)
				if err == nil {
					link := path.Clean(string(bytes))
					fmt.Fprintf(w, "<a href='./%s'>", html.EscapeString(escapeURLPath(link)))
					slash := strings.LastIndex(link, "/")
					if slash >= 0 && slash+1 < len(link) {
						fmt.Fprint(w, "<span class='prefix'>")
						fmt.Fprint(w, html.EscapeString(link[:slash+1]))
						fmt.Fprint(w, "</span>")
						fmt.Fprint(w, html.EscapeString(link[slash+1:]))
					} else {
						fmt.Fprint(w, html.EscapeString(link))
					}
					fmt.Fprint(w, "</a>")
				}
				rc.Close()
			}
			fmt.Fprint(w, "</div>")
		}
		fmt.Fprintf(w, "</td>")
		lines := entry.lines
		if lines >= 0 {
			ls := "line"
			if lines != 1 {
				ls = "lines"
			}
			fmt.Fprintf(w, "<td>%d %s</td>", lines, ls)
		} else {
			fmt.Fprintf(w, "<td class='light'>&mdash;</td>")
		}
		size := float64(entry.file.UncompressedSize64)
		if size >= 10_000_000 {
			fmt.Fprintf(w, "<td>%.0f MB</td>", size/1_000_000)
		} else if size >= 700_000 {
			fmt.Fprintf(w, "<td>%.1f MB</td>", size/1_000_000)
		} else if size >= 10_000 {
			fmt.Fprintf(w, "<td>%.0f KB</td>", size/1000)
		} else if size >= 700 {
			fmt.Fprintf(w, "<td>%.1f KB</td>", size/1000)
		} else {
			bs := "byte"
			if size != 1 {
				bs = "bytes"
			}
			fmt.Fprintf(w, "<td>%.0f %s</td>", size, bs)
		}
		fmt.Fprintf(w, "<td>%s</td>", formatTime(entry.modified))
		fmt.Fprintln(w, "</tr>")
	}
	if len(dir.readmeName) > 0 {
		fmt.Fprintln(w, "        <tr class='border'>")
		fmt.Fprintln(w, "          <td class='category' valign='top'>README</td><td colspan='4' class='readme'><div class='readme-container'>")
		p.writeFileContents(w, dir.entries[dir.readmeName], defaultContentType(dir.entries[dir.readmeName]))
		fmt.Fprintln(w, "          </div></td>")
		fmt.Fprintln(w, "        </tr>")
	}
	fmt.Fprintln(w, "      </table>")
	fmt.Fprintln(w, "    </div>")
	p.writeEpilogue(w)
}

func (p page) writeFilePage(w io.Writer, entry *archiveDirectoryEntry, contentType contentType) {
	p.writePrologue(w)
	p.writeHeader(w, headerOptions{})
	fmt.Fprintln(w, "    <table class='file'>")
	fmt.Fprintln(w, "      <colgroup><col span='1' class='line-numbers-column'><col span='1' width='*'></colgroup>")
	fmt.Fprintln(w, "      <tr class='directory back'><td>&nbsp;</td><td class='filename'><a href='.'>..</a></td></tr>")
	fmt.Fprint(w, "      <tr class='fileborder'>")
	if entry.file.UncompressedSize64 > textFileSizeLimit {
		fmt.Fprint(w, "<td>&nbsp;</td><td><div class='empty'>file is too big to render</div></td>")
	} else {
		if contentType == contentTypeMarkdown {
			fmt.Fprint(w, "<td>&nbsp;</td><td>")
		} else {
			p.writeLineNumbers(w, 1, entry.lines)
			fmt.Fprint(w, "<td valign='top'>")
		}
		p.writeFileContents(w, entry, contentType)
		fmt.Fprint(w, "</td>")
	}
	fmt.Fprintln(w, "</tr>")
	fmt.Fprintln(w, "    </table>")
	p.writeEpilogue(w)
}

func (p page) writeFileContents(w io.Writer, entry *archiveDirectoryEntry, contentType contentType) {
	if entry.lines < 0 {
		fmt.Fprintln(w, "<div class='empty'>binary file</div>")
		return
	} else if entry.file.UncompressedSize64 == 0 {
		fmt.Fprintln(w, "<div class='empty'>empty file</div>")
		return
	}
	rc, err := entry.file.Open()
	if err != nil {
		log.Print(err)
		return
	}
	defer rc.Close()
	if contentType == contentTypeMarkdown {
		fmt.Fprintln(w, "<div class='markdown'>")
		bytes, err := ioutil.ReadAll(rc)
		if err == nil {
			md := goldmark.New(goldmark.WithExtensions(extension.GFM))
			if err := md.Convert(bytes, w); err != nil {
				log.Print(err)
			}
		} else {
			log.Print(err)
		}
		fmt.Fprintln(w, "</div>")
	} else {
		fmt.Fprintln(w, "<pre class='code file-contents'>")
		fmt.Fprint(w, beginSearchMarker)

		buf := &strings.Builder{}
		io.Copy(buf, rc)
		fmt.Fprint(w, html.EscapeString(buf.String()))

		fmt.Fprint(w, endSearchMarker)
		fmt.Fprintln(w, "</pre>")
	}
}

func (p page) writeLineNumbers(w io.Writer, firstLine int, lines int) {
	fmt.Fprint(w, "<td align='right' valign='top'><pre class='code line-numbers'><font color='#acb4bd'>")
	for i := 0; i < lines; i++ {
		fmt.Fprintf(w, "%d\n", firstLine+i)
	}
	fmt.Fprint(w, "</font></pre></td>")
}

func (p page) writeProgressPage(w io.Writer, progress archiveProgress) {
	p.writePrologue(w)
	fmt.Fprintln(w, "    <noscript><meta http-equiv='refresh' content='3'></noscript>")
	downloadProgress := float64(progress.downloadedContentLength) / float64(progress.estimatedContentLength)
	if downloadProgress > 1 {
		// this can happen if the server misreports the file length somehow.
		downloadProgress = 1
	}
	analysisProgress := float64(0)
	if progress.filesToAnalyze > 0 {
		analysisProgress = float64(progress.filesAnalyzed) / float64(progress.filesToAnalyze)
	}
	directoryProgress := float64(0)
	if progress.directories > 0 {
		directoryProgress = float64(progress.renderedDirectories) / float64(progress.directories)
	}
	p.writeHeader(w, headerOptions{
		inProgress: true,
		progress:   0.39*downloadProgress + 0.4*analysisProgress + 0.2*directoryProgress,
	})
	fmt.Fprintln(w, "<div class='markdown'><h1>now loading</h1><p>the archive is currently downloading. this page will reload when the download completes.</p></div>")
	p.writeEpilogue(w)
}

func (p page) writeErrorPage(w io.Writer, err error) {
	p.writePrologue(w)
	p.writeHeader(w, headerOptions{})
	fmt.Fprintln(w, "<div class='markdown'>")
	fmt.Fprintln(w, "<h1>download failed</h1>")
	fmt.Fprintln(w, "<p>here is the error, exactly as it has bubbled up from the depths of the computer:</p>")
	fmt.Fprintln(w, "<p><pre>", html.EscapeString(err.Error()), "</pre></p>")
	if len(p.archiveURL) > 0 {
		fmt.Fprintf(w, "<p>to retry the download, <a href='/%s?remove'>remove this archive</a>, then load the original url again.</p>\n", html.EscapeString(escapeURLPath(p.archiveURL)))
	}
	fmt.Fprintln(w, "</div>")
	p.writeEpilogue(w)
}

func (p page) writeRemoveButtonPage(w io.Writer, message string) {
	p.writePrologue(w)
	p.writeHeader(w, headerOptions{})
	fmt.Fprintln(w, "<div class='markdown'>")
	fmt.Fprintln(w, "<h2>remove rendered files?</h2>")
	fmt.Fprintln(w, "<p>the archive can be re-downloaded and re-rendered by loading the url again.</p>")
	url := html.EscapeString(escapeURLPath(p.archiveURL))
	fmt.Fprintf(w, "<form action='/%s?remove' method='post'><input type='submit' value='remove %s'></form>", url, url)
	if len(message) > 0 {
		fmt.Fprintf(w, "<p>%s</p>\n", html.EscapeString(message))
	}
	fmt.Fprintln(w, "</div>")
	p.writeEpilogue(w)
}

func (p page) writeSearchResultsPage(w io.Writer, query string, filter string, results chan searchResult) {
	p.writePrologue(w)
	p.writeHeader(w, headerOptions{
		searching:    true,
		searchQuery:  query,
		searchFilter: filter,
	})
	fmt.Fprintln(w, "    <table class='search-results'>")
	fmt.Fprintln(w, "      <colgroup><col span='1' class='line-numbers-column'><col span='1' width='*'></colgroup>")
	lastFile := ""
	hadErrors := false
	for result := range results {
		if result.err != nil {
			hadErrors = true
			fmt.Fprintf(w, "<tr class='full-border'><td>&nbsp;</td><td><div class='empty'><b>error</b>&mdash;%s</div></td></tr>\n", html.EscapeString(result.err.Error()))
			continue
		}
		if result.file != lastFile {
			components := strings.Split(result.file, "/")
			if len(components) == 0 {
				continue
			}
			pathHTML := html.EscapeString(components[len(components)-1])
			dir := strings.Join(components[:len(components)-1], "/")
			if dir != "" {
				pathHTML = fmt.Sprintf("<span class='prefix'>%s/</span>%s", html.EscapeString(dir), pathHTML)
			}
			fmt.Fprintf(w, "      <tr><td colspan='2' class='filename'><a href='./%s'>%s</a></td></tr>\n", html.EscapeString(escapeURLPath(result.file)), pathHTML)
			lastFile = result.file
			fmt.Fprint(w, "<tr>")
		} else {
			fmt.Fprint(w, "<tr class='border'>")
		}
		p.writeLineNumbers(w, result.firstLine, result.lines)
		fmt.Fprintf(w, "<td><pre class='code'>\n%s</pre></td></tr>\n", result.html)
	}
	if lastFile == "" && query != "" && !hadErrors {
		msg := "no results found"
		if filter != "" {
			msg = "no results found in paths matching <tt>" + html.EscapeString(filter) + "</tt>"
		}
		fmt.Fprintf(w, "<tr class='full-border'><td>&nbsp;</td><td><div class='empty'>%s</div></td></tr>\n", msg)
	}
	fmt.Fprintln(w, "    </table>")
	p.writeEpilogue(w)
}

// tags to insert around matches in search results.
func searchResultTags(filename string, query string, whichMatch int, globalMatch int) (string, string) {
	return fmt.Sprintf("<a class='search-result' href='%s?search=%s#%d' id='%d'>", html.EscapeString(escapeURLPath(filename)), url.QueryEscape(query), whichMatch, globalMatch), "</a>"
}

func searchAnchorTags(whichMatch int) (string, string) {
	return fmt.Sprintf("<b class='search-result' tabindex='0' title='key shortcut: j/k to select next/prev result' id='%d'>", whichMatch), "</b>"
}

func (p page) writePrologue(w io.Writer) {
	fmt.Fprintln(w, "<!doctype html>")
	fmt.Fprintln(w, "<html>")
	fmt.Fprintln(w, "  <head>")
	fmt.Fprintln(w, "    <meta charset='utf-8'>")
	fmt.Fprintln(w, "    <meta name='viewport' content='initial-scale=0.9'>")
	fmt.Fprintln(w, "    <link href='/style.css' rel='stylesheet'>")
	fmt.Fprintln(w, "    <link rel='stylesheet' href='//cdnjs.cloudflare.com/ajax/libs/highlight.js/10.7.2/styles/tomorrow.min.css'>")
	fmt.Fprintln(w, "    <script src='//cdnjs.cloudflare.com/ajax/libs/highlight.js/10.7.2/highlight.min.js'></script>")
	fmt.Fprintln(w, "    <script>hljs.highlightAll();</script>")
}

type headerOptions struct {
	inProgress bool
	progress   float64

	searching    bool
	searchQuery  string
	searchFilter string
}

func (p page) writeHeader(w io.Writer, o headerOptions) {
	components := strings.Split(p.name, "/")
	if p.name == "" {
		components = nil
	}
	depth := len(components)
	if !p.isDirectory {
		depth--
	}
	rootPath := "/"
	if depth >= 0 {
		rootPath = "./" + strings.Repeat("../", depth)
	}
	archiveComponents := strings.Split(p.archiveURL, "/")
	archiveShortName := archiveComponents[len(archiveComponents)-1]
	if o.searching && len(o.searchQuery) > 0 {
		fmt.Fprintf(w, "    <title>searching for &ldquo;%s&rdquo; in %s", html.EscapeString(o.searchQuery), html.EscapeString(archiveShortName))
	} else if o.searching {
		fmt.Fprintf(w, "    <title>searching in %s", html.EscapeString(archiveShortName))
	} else if len(components) > 0 {
		fmt.Fprintf(w, "    <title>%s in %s", html.EscapeString(components[len(components)-1]), html.EscapeString(archiveShortName))
		for _, v := range components[:len(components)-1] {
			fmt.Fprintf(w, "/%s", html.EscapeString(v))
		}
	} else {
		fmt.Fprintf(w, "    <title>%s", html.EscapeString(archiveShortName))
	}
	fmt.Fprintln(w, " - dezip.org</title>")
	fmt.Fprintln(w, "  </head>")
	fmt.Fprintln(w, "  <body>")

	fmt.Fprint(w, "    <pre class='header' id='path-header'><a class='logo' href='/'>", logo, "</a>")
	if o.searching {
		fmt.Fprint(w, "<b><i>searching in </i></b>")
	}
	if depth == 0 && !o.searching && p.isDirectory {
		fmt.Fprintf(w, "<b>%s</b>", html.EscapeString(p.archiveURL))
	} else {
		fmt.Fprintf(w, "<a href='%s'>%s</a>", rootPath, html.EscapeString(archiveShortName))
	}
	if o.searching {
		if len(o.searchFilter) > 0 {
			fmt.Fprintf(w, "<b id='search-filter'> [%s]</b>", html.EscapeString(o.searchFilter))
		} else {
			fmt.Fprint(w, "<b id='search-filter'>...</b>")
		}
	}
	for i, v := range components {
		if i == len(components)-1 {
			fmt.Fprintf(w, " / <b>%s</b>", html.EscapeString(v))
		} else if depth-i-1 == 0 {
			fmt.Fprintf(w, " / <a href='.'>%s</a>", html.EscapeString(v))
		} else {
			fmt.Fprintf(w, " / <a href='%s'>%s</a>", strings.Repeat("../", depth-i-1), html.EscapeString(v))
		}
	}
	searchIcon := "<svg width='0' height='0' style='width: 19px' viewBox='0 -0.5 19 18.5' " +
		"fill='none' xmlns='http://www.w3.org/2000/svg'><path d='M10.5872 " +
		"10.5595C11.545 9.54455 12.1335 8.16753 12.1335 6.65098C12.1335 3.53003 " +
		"9.64121 1 6.56677 1C3.49233 1 1 3.53003 1 6.65098C1 9.77193 3.49233 " +
		"12.302 6.56677 12.302C8.14726 12.302 9.57391 11.6333 10.5872 " +
		"10.5595ZM10.5872 10.5595L18 18' stroke-width='2'/></svg>"
	if !o.searching {
		fmt.Fprintf(w, "  <a href='%s?search' class='search-button' id='open-search' aria-label='search'>", rootPath)
		fmt.Fprint(w, searchIcon)
		fmt.Fprint(w, "</a>")
	}
	if o.inProgress {
		fmt.Fprintf(w, "<span id='progress-overlay' style='left: %f%%'></span>", o.progress*100)
	}
	fmt.Fprintln(w, "</pre>")
	fmt.Fprint(w, "    <pre class='header")
	if o.searching {
		fmt.Fprint(w, " searching")
	}
	fmt.Fprint(w, "' id='search-header'>")
	fmt.Fprint(w, "<a href='javascript:void(0);' class='search-button' id='submit-search' aria-label='submit'>")
	fmt.Fprint(w, searchIcon)
	fmt.Fprint(w, "</a>")
	fmt.Fprintf(w, "<form action='%s' method='get' id='search-form' rel='noopener'>", rootPath)
	autofocus := ""
	if o.searching && o.searchQuery == "" {
		autofocus = " autofocus"
	}
	fmt.Fprintf(w, "<input type='text' placeholder='search files in %s' value='%s' name='search' id='search-field' autocapitalize='none' autocorrect='off' autocomplete='off'%s>", html.EscapeString(archiveShortName), html.EscapeString(o.searchQuery), autofocus)
	fmt.Fprint(w, "</form>&nbsp;")
	if !o.searching {
		fmt.Fprint(w, "<a href='javascript:void(0);' class='search-button' id='cancel-search' aria-label='cancel search'>")
		fmt.Fprint(w, "<svg width='0' height='0' style='width: 16px' viewBox='0 0 16 16' fill='none' xmlns='http://www.w3.org/2000/svg'>")
		fmt.Fprint(w, "<path d='M15 15L8 8M1 1L8 8M8 8L15 1M8 8L1 15' stroke-width='2'/>")
		fmt.Fprint(w, "</svg></a>")
	}
	fmt.Fprintln(w, "</pre>")
	fmt.Fprintln(w, "    <script src='/dezip.js'></script>")
}

func (p page) writeEpilogue(w io.Writer) {
	fmt.Fprintln(w, "  </body>")
	fmt.Fprintln(w, "</html>")
}

func formatTime(t time.Time) string {
	return strings.ToLower(t.Format("<span class='abbr abbr-January'><span>January</span></span> 2, 2006"))
}

func escapeURLPath(p string) string {
	return (&url.URL{Path: p}).EscapedPath()
}

const logo = `<svg width='0' height='0' style='width: 24px' viewBox='0 0 24
 12' stroke='none' xmlns='http://www.w3.org/2000/svg'><path fill-rule='evenodd'
 clip-rule='evenodd' d='M4 8.5V9.0975C4 10.1632 4.83571 11.0418 5.90013
 11.095L19.8002 11.79C22.085 11.9042 24 10.0826 24 7.795V4.205C24 1.91741
 22.085 0.095751 19.8002 0.209988L5.90012 0.904994C4.83571 0.958215 4 1.83675 4
 2.9025V3.5H6V3.5C6 3.21863 6.21815 2.98546 6.49889 2.96674L10.0078
 2.73282C11.8977 2.60682 13.5 4.10586 13.5 6V6C13.5 7.89414 11.8977 9.39318
 10.0078 9.26718L6.49889 9.03326C6.21815 9.01454 6 8.78137 6 8.5V8.5H4ZM15.5
 8.57987C15.5 9.09996 15.8987 9.53322 16.417 9.57641L20.1645 9.88871C20.8834
 9.94862 21.5 9.3813 21.5 8.65991V8.65991C21.5 8.24764 21.294 7.86264 20.9509
 7.63395L20.9339 7.62257C20.4004 7.2669 20.2152 6.56963 20.5019 5.99614L21.3404
 4.31922C21.4454 4.10929 21.5 3.87781 21.5 3.6431V3.6431C21.5 2.75859 20.744
 2.063 19.8626 2.13645L16.191 2.44242C15.8004 2.47496 15.5 2.80147 15.5
 3.19339V3.31958C15.5 3.74467 15.7124 4.14163 16.0661 4.37743V4.37743C16.5996
 4.7331 16.7848 5.43037 16.4981 6.00386L15.7111 7.57771C15.5723 7.85542 15.5
 8.16165 15.5 8.47214V8.57987Z'/><path d='M7 7.5C7.55228 7.5 8 7.05228 8
 6.5V5.5C8 4.94772 7.55228 4.5 7 4.5H5L4 4.5H1C0.447715 4.5 0 4.94772 0 5.5L0
 6.5C0 7.05228 0.447715 7.5 1 7.5H4L5 7.5H7Z'/></svg>`
