package main

import (
    "archive/zip"
    "bufio"
    "bytes"
    "crypto/sha256"
    "encoding/binary"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "hash/crc32"
    "io"
    "io/ioutil"
    "log"
    "os"
    "path"
    "runtime/debug"
    "sort"
    "strings"
    "sync"
    "time"
)

type archive struct {
    mutex sync.Mutex

    // the current state of the archive -- see archiveState below.
    state archiveState

    // the path within the cache rootPath where this archive is stored.  always
    // begins with a slash.
    path string

    // the time when this archive was created.
    creationTime time.Time

    // updated as downloading progresses.
    progress archiveProgress

    // these are set during the downloading state and used while rendering.
    directories map[string]*archiveDirectory
    filesToRender []*archiveDirectoryEntry
    filesToRenderByName map[string]int

    // if the root directory just has a single subdirectory, that's the
    // initialDirectory.  redirect here when the archive is first loaded.
    initialDirectory string

    // the renderedFiles array is used to signal when a file is finished
    // rendering.  every time a file is rendered, the associated channel is
    // closed (or a singleton closed channel is added).  note that this includes
    // directories, which are rendered during the downloading state.
    renderedFiles map[string]chan struct{}

    // this channel is closed when the archive is finished being downloaded.
    downloaded chan struct{}

    // renderers update these as they progress through the filesToRender array.
    // when filesBeingRendered is 0 and nextFileToRender is len(filesToRender),
    // rendering is finished.
    nextFileToRender int
    filesBeingRendered int
    // the number of files which have been reordered to the next index.  without
    // this, every http request would swap its priority file to the same index,
    // clobbering other requests in progress.
    priorityFiles int

    // file contents are stored in a zip file until rendering is finished.  keep
    // a reference to the zip reader so it can be closed after it is no longer
    // needed.
    zipReadCloser *zip.ReadCloser
    // remember the path of the zip file so it can be deleted.
    zipFileName string

    // set when transitioning to archiveStateFailed.
    failureReason error

    // a search acceleration data structure.  see search.go.
    searchIndex *searchIndex
}

// as the archive is downloaded, then its files are rendered, it progresses
// through these states.  accessing a "downloading" archive will show a progress
// bar.  once the archive is "rendering", you can browse around as files are
// rendered in the background -- requesting an unrendered file will prioritize
// its rendering.
type archiveState int
const (
    archiveStateInitial archiveState = iota
    archiveStateDownloading
    archiveStateRendering
    archiveStateFinished
    archiveStateFailed
)

func newArchive(path string) *archive {
    globalMutex.Lock()
    defer globalMutex.Unlock()
    if activeArchives >= activeArchiveLimit {
        return nil
    }
    activeArchives++
    return &archive{
        path: path,
        renderedFiles: make(map[string]chan struct{}),
        downloaded: make(chan struct{}),
    }
}

func (ar *archive) transitionToState(state archiveState) {
    if ar.state == state {
        return
    }
    if ar.state > state {
        log.Print("ignoring invalid state transition %v -> %v for archive at %s\n", ar.state, state, ar.path)
        return
    }
    wasFinishedOrFailed := ar.state == archiveStateFinished || ar.state == archiveStateFailed
    ar.state = state
    if state == archiveStateFinished {
        writeMetadataChecksum(ar.searchIndex.file)
    }
    if state == archiveStateDownloading {
        ar.directories = make(map[string]*archiveDirectory)
        ar.filesToRenderByName = make(map[string]int)
    }
    if !wasFinishedOrFailed && (state == archiveStateFinished || state == archiveStateFailed) {
        globalMutex.Lock()
        activeArchives--
        globalMutex.Unlock()
        ar.directories = nil
        ar.filesToRender = nil
        ar.filesToRenderByName = nil
        ar.renderedFiles = nil
        if ar.zipReadCloser != nil {
            ar.zipReadCloser.Close()
            ar.zipReadCloser = nil
        }
        if len(ar.zipFileName) > 0 {
            os.Remove(ar.zipFileName)
            ar.zipFileName = ""
        }
    }
    if state == archiveStateFailed {
        if ar.searchIndex != nil {
            ar.searchIndex.close()
        }
    }
}

type archiveProgress struct {
    // an estimate of the number of bytes in the file.  may be (much) larger
    // than the real size.
    estimatedContentLength int64
    // how many bytes of the file have been downloaded?
    downloadedContentLength int64
    // after downloading a zip, we have to analyze each file to determine how
    // many lines it has.
    filesToAnalyze int
    filesAnalyzed int
    // how many directories are there?
    directories int
    // how many directories have been rendered?
    renderedDirectories int
}

type archiveDirectory struct {
    entries map[string]*archiveDirectoryEntry

    directoryNames []string
    fileNames []string
    readmeName string
}

type archiveDirectoryEntry struct {
    // nil for directories.
    file *zip.File

    // directories don't have a zip.File -- track the modification date
    // separately.
    modified time.Time

    // a negative number of lines means this is a binary file (or too big to
    // render).
    lines int

    // the maximum length of any line in this file.
    maximumLineLength int
}

func (ar *archive) addDirectoryEntry(file *archiveDirectoryEntry, components []string) error {
    var newDirectoryEntries uint64
    for index, component := range components {
        // create the containing directory.
        path := components[:index]
        key := strings.Join(path, "/")
        dir := ar.directories[key]
        if dir == nil {
            dir = &archiveDirectory{ entries: make(map[string]*archiveDirectoryEntry) }
            ar.directories[key] = dir
        }
        // check for empty path components.
        isIntermediateDirectory := index != len(components) - 1
        if len(component) == 0 {
            if index == 0 {
                return fmt.Errorf("addDirectoryEntry(): absolute path %v not allowed", strings.Join(components, "/"))
            } else if isIntermediateDirectory {
                return fmt.Errorf("addDirectoryEntry(): empty directory not allowed in path %v", strings.Join(components, "/"))
            } else {
                // if the last component is empty, this is just a directory
                // name.  there's nothing left to do here.
                break
            }
        }
        if isIntermediateDirectory {
            // keep track of the most recent modification date for each
            // intermediate directory.  also count the total number of lines
            // because why not.
            entry := dir.entries[component]
            if entry == nil {
                entry = &archiveDirectoryEntry{}
                dir.entries[component] = entry
                newDirectoryEntries++
            }
            if file.modified.After(entry.modified) {
                entry.modified = file.modified
            }
            entry.lines += file.lines
        } else {
            // this is the entry for the file itself.
            dir.entries[component] = file
            newDirectoryEntries++
        }
        if len(dir.entries) > entriesPerDirectoryLimit {
            return fmt.Errorf("addDirectoryEntry(): directory '%s' has more than %d entries", key, entriesPerDirectoryLimit)
        }
    }
    return nil
}
func (ar *archive) requestRender(name string) chan struct{} {
    if ar.state == archiveStateFinished {
        // assume the file is there.
        return closedChannel
    } else if ar.state == archiveStateFailed {
        // return a nil channel so reading blocks and control flow proceeds down
        // down the failure path.
        return nil
    }
    // look up the channel used to determine whether a file has been rendered.
    rendered := ar.renderedFiles[name]
    if rendered == nil {
        rendered = make(chan struct{})
        ar.renderedFiles[name] = rendered
    }
    if ar.state == archiveStateRendering {
        select {
        case <-rendered:
            // the file is already rendered; there's nothing to do.
            break
        default:
            // move the requested file to the next index in the
            // filesToRender slice so it's rendered soon.
            index, ok := ar.filesToRenderByName[name]
            priorityIndex := ar.nextFileToRender + ar.priorityFiles
            if ok && index > priorityIndex {
                fs := ar.filesToRender
                file := fs[index]
                fs[index] = fs[priorityIndex]
                fs[priorityIndex] = file
                ar.filesToRenderByName[fs[index].file.Name] = index
                ar.filesToRenderByName[fs[priorityIndex].file.Name] = priorityIndex
                ar.priorityFiles++
            }
        }
    }
    return rendered
}
func (ar *archive) notifyRendered(name string) {
    if ch, ok := ar.renderedFiles[name]; ok {
        close(ch)
    } else {
        ar.renderedFiles[name] = closedChannel
    }
}

func (c *cache) download(p page, format archiveFormat, protocol protocol, archive *archive) error {
    res, err := protocol.fetch(p.archiveURL)
    if err != nil {
        return err
    }
    defer res.body.Close()
    if res.contentLength > archiveSizeLimit {
        return fmt.Errorf("archive exceeded maximum size of %d bytes", archiveSizeLimit)
    }
    if res.contentLength > 0 {
        archive.mutex.Lock()
        archive.progress.estimatedContentLength = res.contentLength
        archive.mutex.Unlock()
    }
    // create a temporary zip file to download into.  all types of archives are
    // converted to zip as they're downloaded.  then files are rendered out of
    // the zip -- this would be difficult to do with tar, which doesn't support
    // random access.
    f, err := ioutil.TempFile("", "dezip.*.zip")
    if err != nil {
        return err
    }
    defer f.Close()
    archive.mutex.Lock()
    archive.zipFileName = f.Name()
    archive.mutex.Unlock()
    // actually download the file.  track progress via an io.TeeReader.
    err = format.download(f, io.TeeReader(res.body, progressWriter{ archive }))
    if err != nil {
        return err
    }
    // read the zip directory structure into memory.
    rc, err := zip.OpenReader(f.Name())
    if err != nil {
        return err
    }

    // create the archive metadata file.
    metadataPath := c.archiveMetadataPath(archive.path)
    searchIndex, err := createSearchIndex(metadataPath, len(rc.File))

    archive.mutex.Lock()
    archive.zipReadCloser = rc
    archive.progress.filesToAnalyze = len(rc.File)
    archive.searchIndex = searchIndex
    archive.mutex.Unlock()
    var buffer bytes.Buffer
    var totalSize uint64
    for _, file := range rc.File {
        totalSize += file.UncompressedSize64
        if totalSize > uncompressedArchiveSizeLimit {
            return fmt.Errorf("uncompressed archive size exceeded limit of %d bytes", uncompressedArchiveSizeLimit)
        }
        if len(file.Name) > archivePathLimit {
            return fmt.Errorf("length of filename %s greater than limit %d", file.Name, archivePathLimit)
        }
        components := strings.SplitN(file.Name, "/", archiveComponentLimit + 1)
        if len(components) > archiveComponentLimit {
            return fmt.Errorf("number of path components in %s greater than limit %d", file.Name, archiveComponentLimit)
        }
        invalidComponent := findInvalidComponent(components)
        if invalidComponent == indexFileName {
            // just ignore files and directories that match the
            // index file name. otherwise these files/directories
            // would overwrite the directory index pages.
            continue
        } else if len(invalidComponent) > 0 {
            return fmt.Errorf("filename %s contains a . or ..", file.Name)
        }
        entry := &archiveDirectoryEntry{
            file: file,
            modified: file.Modified,
            lines: -1,
        }
        if strings.HasSuffix(file.Name, "/") {
            entry.file = nil
        }
        if file.UncompressedSize64 <= textFileSizeLimit && entry.file != nil {
            rc, err := entry.file.Open()
            if err == nil {
                buffer.Reset()
                _, err := buffer.ReadFrom(rc)
                rc.Close()
                if err == nil {
                    // count the number of lines in the file.  if the file
                    // doesn't look like text, set the number of lines to a
                    // negative number.
                    weirdCharacters := 0
                    entry.lines = 1
                    contents := buffer.Bytes()
                    blankLine := true
                    lineLength := 0
                    for i := 0; i < len(contents); i++ {
                        switch contents[i] {
                        case '\r':
                            if i + 1 < len(contents) && contents[i + 1] == '\n' {
                                i++
                            }
                            fallthrough
                        case '\n':
                            entry.lines++
                            if lineLength > entry.maximumLineLength {
                                entry.maximumLineLength = lineLength
                            }
                            lineLength = 0
                            blankLine = true
                        case 0:
                            // this isn't utf-8 text.
                            entry.lines = -1
                        default:
                            if contents[i] > 0xf4 {
                                // this isn't utf-8 text, but some files in the
                                // linux source tree use non-utf-8 codepages.
                                // so we allow a few illegal characters through
                                // (they'll show up as 0xFFFD on the web).
                                weirdCharacters++
                                if weirdCharacters > weirdCharacterLimit {
                                    entry.lines = -1
                                    break
                                }
                            }
                            lineLength++
                            blankLine = false
                        }
                        if entry.lines < 0 {
                            break
                        }
                    }
                    if lineLength > entry.maximumLineLength {
                        entry.maximumLineLength = lineLength
                    }
                    if blankLine {
                        entry.lines--
                    }
                    if entry.lines >= 0 {
                        searchIndex.addFile(entry.file.Name, contents)
                    }
                }
            }
        }
        archive.mutex.Lock()
        if entry.file != nil {
            // add this file to the list of files that the renderer goroutines
            // will consume.
            archive.filesToRenderByName[entry.file.Name] = len(archive.filesToRender)
            archive.filesToRender = append(archive.filesToRender, entry)
        }
        err := archive.addDirectoryEntry(entry, components)
        archive.progress.filesAnalyzed++
        archive.mutex.Unlock()
        if err != nil {
            return err
        }
    }
    archive.mutex.Lock()
    archive.progress.estimatedContentLength = archive.progress.downloadedContentLength
    archive.progress.directories = len(archive.directories)
    // if there's only one directory entry in the root directory, set it as the
    // initial directory.
    for {
        dir := archive.directories[archive.initialDirectory]
        if dir == nil || len(dir.entries) != 1 {
            break
        }
        var name string
        for name, _ = range dir.entries {}
        if dir.entries[name].file != nil {
            break
        }
        archive.initialDirectory = path.Join(archive.initialDirectory, name)
    }
    // first pass through the directories.  separate and sort directories and
    // files and discover any readme files.
    for _, v := range archive.directories {
        archive.mutex.Unlock()
        v.fileNames = []string{}
        v.directoryNames = []string{}
        for name, entry := range v.entries {
            if entry.file == nil {
                v.directoryNames = append(v.directoryNames, name)
            } else {
                v.fileNames = append(v.fileNames, name)
                if entry.lines > 0 && useAsReadme(v, name) {
                    v.readmeName = name
                }
            }
        }
        sort.Strings(v.fileNames)
        sort.Strings(v.directoryNames)
        archive.mutex.Lock()
    }
    // second pass through the directories.  render the directory index pages.
    for k, _ := range archive.directories {
        archive.mutex.Unlock()
        if err := os.MkdirAll(fmt.Sprintf("%s%s/%s", c.rootPath, archive.path, k), 0755); err != nil {
            return err
        }
        f, err := os.Create(fmt.Sprintf("%s%s/%s/%s", c.rootPath, archive.path, k, indexFileName))
        if err != nil {
            return err
        }
        w := bufio.NewWriter(f)
        dp := page{ name: k, isDirectory: true, archiveURL: p.archiveURL }
        dp.writeDirectoryPage(w, archive.directories)
        w.Flush()
        f.Close()
        archive.mutex.Lock()
        archive.notifyRendered(k)
        archive.progress.renderedDirectories++
    }
    // finish writing the metadata file.
    metadata := archiveMetadata{
        Version: 1,
        ArchivePath: archive.path,
        ArchiveURL: p.archiveURL,
        CreationTime: archive.creationTime,
        NumberOfFiles: len(rc.File),
        InitialDirectory: archive.initialDirectory,
    }
    if err := metadata.writeToFile(searchIndex.file); err != nil {
        archive.mutex.Unlock()
        return err
    }
    // notify any waiting goroutines that the archive has been downloaded.
    close(archive.downloaded)

    if len(archive.filesToRender) > 0 {
        // the download is finished.  transition to the rendering state.
        archive.transitionToState(archiveStateRendering)
        archive.mutex.Unlock()
        c.mutex.Lock()
        c.renderingArchiveURLs = append(c.renderingArchiveURLs, p.archiveURL)
        c.renderingCond.Broadcast()
        c.mutex.Unlock()
    } else {
        // there aren't any files to render, so transition to the finished state
        // directly (the renderers won't do it unless they actually finish
        // rendering something).
        archive.transitionToState(archiveStateFinished)
        archive.mutex.Unlock()
    }
    return nil
}

func findInvalidComponent(components []string) string {
    invalidComponent := ""
    for _, v := range components {
        if v == "." || v == ".." {
            return v
        } else if v == indexFileName {
            invalidComponent = v
        }
    }
    return invalidComponent
}

func isMarkdown(name string) bool {
    return strings.HasSuffix(strings.ToLower(name), ".md")
}

func defaultContentType(entry *archiveDirectoryEntry) contentType {
    if isMarkdown(entry.file.Name) {
        return contentTypeMarkdown
    } else {
        return contentTypeText
    }
}

func useAsReadme(dir *archiveDirectory, name string) bool {
    lower := strings.ToLower(name)
    if !strings.Contains(lower, "readme") {
        return false
    }
    // don't show html source code.
    if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") {
        return false
    }
    if dir.readmeName == "" {
        return true
    }
    // prefer markdown readme files.
    if isMarkdown(name) && !isMarkdown(dir.readmeName) {
        return true
    } else if !isMarkdown(name) && isMarkdown(dir.readmeName) {
        return false
    }
    // otherwise, prefer shorter file names.
    if len(name) < len(dir.readmeName) {
        return true;
    } else if len(name) > len(dir.readmeName) {
        return false;
    }
    return strings.Compare(name, dir.readmeName) >= 0
}

type progressWriter struct {
    ar *archive
}
func (w progressWriter) Write(p []byte) (n int, err error) {
    n = len(p)
    w.ar.mutex.Lock()
    w.ar.progress.downloadedContentLength += int64(n)
    if w.ar.progress.downloadedContentLength > archiveSizeLimit {
        err = fmt.Errorf("archive exceeded maximum size of %d bytes", archiveSizeLimit)
    }
    w.ar.mutex.Unlock()
    return
}

// -- metadata

type archiveMetadata struct {
    Version int
    ArchivePath string
    ArchiveURL string
    CreationTime time.Time
    NumberOfFiles int
    InitialDirectory string
}

func (m archiveMetadata) writeToFile(file *os.File) error {
    // metadata appears at the end -- the search index has already written
    // itself to the beginning of the file.
    if _, err := file.Seek(0, io.SeekEnd); err != nil {
        return err
    }
    bytes, err := json.Marshal(m)
    if err != nil {
        return err
    }
    if len(bytes) > 0x7fffffff {
        return fmt.Errorf("archiveMetadata.writeToFile(): encoded json metadata too large")
    }
    // write the json, then the length of the json string.  the length is
    // written to make it easier to find the beginning of the json string
    // (although it's technically unnecessary... you could always parse the json
    // in reverse).
    if _, err := file.Write(bytes); err != nil {
        return err
    }
    // note that the length has to be representable in 32 bits, which we check
    // above (that's what the 0x7fffffff is for).
    if err := binary.Write(file, binary.LittleEndian, int32(len(bytes))); err != nil {
        return err
    }
    return nil
}

func writeMetadataChecksum(file *os.File) error {
    // once the archive is finished rendering, write a checksum to detect any
    // faults (sudden crash or power loss) that would produce an invalid,
    // partially-written file.
    if _, err := file.Seek(0, io.SeekStart); err != nil {
        return err
    }
    h := crc32.NewIEEE()
    if _, err := io.Copy(h, file); err != nil {
        return err
    }
    // is this ever actually necessary?
    if _, err := file.Seek(0, io.SeekEnd); err != nil {
        return err
    }
    return binary.Write(file, binary.LittleEndian, h.Sum32())
}

func readMetadata(file *os.File) (archiveMetadata, error) {
    var m archiveMetadata
    // measure the length of the file by file.Seek()ing to the end.
    n, err := file.Seek(0, io.SeekEnd)
    if err != nil {
        return m, err
    }
    // compute the file checksum.
    if _, err := file.Seek(0, io.SeekStart); err != nil {
        return m, err
    }
    h := crc32.NewIEEE()
    // io.LimitReader is used to avoid including the checksum itself in the
    // checksum computation (the checksum is 4 bytes long).
    if _, err := io.Copy(h, io.LimitReader(file, n - 4)); err != nil && err != io.EOF {
        return m, err
    }
    // read the expected checksum from the file and compare it with the one just
    // computed.
    var checksum uint32
    if err := binary.Read(file, binary.LittleEndian, &checksum); err != nil && err != io.EOF {
        return m, err
    }
    if checksum != h.Sum32() {
        return m, fmt.Errorf("readMetadata(): bad checksum for file %s (%x vs %x)", file.Name(), checksum, h.Sum32())
    }
    // decode the json metadata -- the length that was encoded in writeToFile()
    // lets us grab the exact bytes to be decoded using ReadAt().
    if _, err := file.Seek(-8, io.SeekEnd); err != nil {
        return m, err
    }
    var metadataLength int32
    if err := binary.Read(file, binary.LittleEndian, &metadataLength); err != nil && err != io.EOF {
        return m, err
    }
    encoded := make([]byte, metadataLength)
    if l, err := file.ReadAt(encoded, n - 8 - int64(metadataLength)); l != int(metadataLength) {
        return m, err
    }
    if err := json.Unmarshal(encoded, &m); err != nil {
        return m, err
    }
    return m, nil
}

func (c *cache) archiveMetadataPath(archivePath string) string {
    // the point of this function is to construct a recognizable path that won't
    // collide with other paths.  you could just use the sha256 hash, but then
    // all the metadata paths would look indistinguishable... which could be
    // annoying if you're trying to debug something.
    metadataPath := archivePath
    if len(metadataPath) > 64 {
        metadataPath = metadataPath[:64]
    }
    metadataPath = alphanum.ReplaceAllLiteralString(metadataPath, "_")
    sum := sha256.Sum256([]byte(metadataPath))
    metadataPath += "_" + hex.EncodeToString(sum[:])
    return path.Join(c.metaPath, metadataPath)
}

func loadArchivesFromMetadata(metaPath string) (map[string]*archive, error) {
    // iterate over all the files in the cache's metaPath, creating an archive
    // for each valid metadata file.
    archivesByURL := make(map[string]*archive)
    os.Mkdir(metaPath, 0755)
    metadataFiles, err := ioutil.ReadDir(metaPath)
    if err != nil {
        return nil, err
    }
    for _, info := range metadataFiles {
        path := path.Join(metaPath, info.Name())
        file, err := os.Open(path)
        if err != nil {
            return nil, err
        }
        metadata, err := readMetadata(file)
        file.Close()
        if err != nil {
            log.Print("metadata read error: ", err)
            os.Remove(path)
            continue
        }
        searchIndex, err := openSearchIndex(path, metadata.NumberOfFiles)
        if err != nil {
            log.Print("search index read error: ", err)
            os.Remove(path)
            continue
        }
        archivesByURL[metadata.ArchiveURL] = &archive{
            state: archiveStateFinished,
            path: metadata.ArchivePath,
            creationTime: metadata.CreationTime,
            initialDirectory: metadata.InitialDirectory,
            downloaded: closedChannel,
            searchIndex: searchIndex,
        }
    }
    return archivesByURL, nil
}

// -- renderer

type renderer struct {}

func newRenderer() *renderer {
    return &renderer{}
}

func (r *renderer) renderLoop(c *cache) {
    for {
        c.mutex.Lock()
        var ar *archive
        var archiveURL string
        for ar == nil {
            // prioritize archives with more priorityFiles.  if two archives
            // have the same number of priorityFiles, render from the one that
            // began rendering first (appears earlier in the list).
            priorityFiles := -1
            // remove any out-of-date archive URLs by building a new list here.
            renderingArchiveURLs := []string{}
            for _, url := range c.renderingArchiveURLs {
                v := c.archivesByURL[url]
                if v == nil || v.state != archiveStateRendering {
                    continue
                }
                renderingArchiveURLs = append(renderingArchiveURLs, url)
                v.mutex.Lock()
                if v.nextFileToRender < len(v.filesToRender) && v.priorityFiles > priorityFiles {
                    ar = v
                    archiveURL = url
                    priorityFiles = v.priorityFiles
                }
                v.mutex.Unlock()
            }
            c.renderingArchiveURLs = renderingArchiveURLs
            if ar == nil {
                // if there's nothing to render, block until there is.
                c.renderingCond.Wait()
            }
        }
        c.mutex.Unlock()

        ar.mutex.Lock()
        // since the lock wasn't held this whole time, check again that this
        // archive has files to render.
        if ar.state != archiveStateRendering || ar.nextFileToRender >= len(ar.filesToRender) {
            ar.mutex.Unlock()
            continue
        }
        // take responsibility for rendering the next file in the list.
        fileToRender := ar.filesToRender[ar.nextFileToRender]
        ar.nextFileToRender++
        if ar.priorityFiles > 0 {
            log.Print("rendering priority file: ", fileToRender.file.Name)
            ar.priorityFiles--
        }
        ar.filesBeingRendered++
        ar.mutex.Unlock()

        // actually render the file.
        contentType := defaultContentType(fileToRender)
        err := r.render(path.Join(c.rootPath, ar.path, fileToRender.file.Name), archiveURL, fileToRender, contentType)
        if err != nil {
            log.Print("error during render(): ", err)
        }
        // render markdown files a second time as text so they can be searched.
        if contentType != contentTypeText {
            filename := path.Join(c.textPath, ar.path, fileToRender.file.Name)
            err := os.MkdirAll(path.Dir(filename), 0755)
            if err == nil {
                err = r.render(filename, archiveURL, fileToRender, contentTypeText)
            }
            if err != nil {
                log.Print("error during textual render(): ", err)
            }
        }

        ar.mutex.Lock()
        if ar.state != archiveStateRendering {
            ar.mutex.Unlock()
            continue
        }
        ar.filesBeingRendered--
        // signal to any waiting goroutines that the file has rendered.
        ar.notifyRendered(fileToRender.file.Name)
        // check whether rendering is finished.
        if ar.filesBeingRendered == 0 && ar.nextFileToRender >= len(ar.filesToRender) {
            ar.transitionToState(archiveStateFinished)
        }
        ar.mutex.Unlock()
    }
}

func (r *renderer) render(filename string, archiveURL string, entry *archiveDirectoryEntry, contentType contentType) (err error) {
    defer func () {
        if r := recover(); r != nil {
            err = fmt.Errorf("panic during render: %v\n%v", r, string(debug.Stack()))
        }
    }()
    var f *os.File
    f, err = os.Create(filename)
    if err != nil {
        log.Print(err)
        return
    }
    defer f.Close()
    w := bufio.NewWriter(f)
    defer w.Flush()
    p := page{ name: entry.file.Name, archiveURL: archiveURL }
    p.writeFilePage(w, entry, contentType)
    return
}
