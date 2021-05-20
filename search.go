package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
	"runtime/debug"
	"syscall"
	"time"
)

const beginSearchMarker = "<!-- BEGIN SEARCH -->"
const endSearchMarker = "<!-- END SEARCH -->"

// how many lines of context should be shown before and after a match?  e.g.,
// if `matchContextLinesBefore` is 1 and `matchContextLinesAfter` is 2, search
// results will look something like:
//   1  ...
//   2  [match]
//   3  ...
//   4  ...
const matchContextLinesBefore = 3
const matchContextLinesAfter = 3

type searchResult struct {
	file      string
	firstLine int
	lines     int
	html      string

	err error
}

type searchLineType int

const (
	lineTypeNoMatch searchLineType = iota
	lineTypeMatch

	// the bytes in the file before the beginSearchMarker and after the
	// endSearchMarker.  all the bytes are represented as a single "line", no
	// matter how many newlines are in there.
	lineTypeSurrounding
)

type searchResultLine struct {
	lineType searchLineType
	bytes    []byte
}

func matchLines(content []byte, query string, tags func() (string, string), visit func(searchResultLine)) {
	buf := content
	// find the searchable region of the file by looking for the
	// begin and end search markers.
	start := bytes.Index(buf, []byte(beginSearchMarker))
	if start < 0 {
		// if the begin search marker doesn't appear in the file, then the
		// entire file is just surrounding text.
		visit(searchResultLine{lineTypeSurrounding, content})
		return
	}
	before := buf[:start+len(beginSearchMarker)]
	buf = buf[start+len(beginSearchMarker):]
	end := bytes.LastIndex(buf, []byte(endSearchMarker))
	if end < 0 {
		// this shouldn't happen, but treat the entire file as surrounding text
		// if it does.
		visit(searchResultLine{lineTypeSurrounding, content})
		return
	}
	after := buf[end:]
	buf = buf[:end]

	visit(searchResultLine{lineTypeSurrounding, before})

	// loop over the lines of the file, looking for the query
	// string.  this code makes a few assumptions about the file's
	// html:
	// - html.EscapeString (or something equivalent) is used to add
	//   the html entities.
	// - newlines are normalized to '\n' characters.
	// - < only appears at the beginning of a tag, and > only appears at the end
	//   of a tag.
	// - all elements are formatting elements -- if a match spans multiple
	//   elements, the user agent will need to run the "adoption agency
	//   algorithm" in order for the result to look reasonable.
	// - since results are written line-by-line, elements cannot span multiple
	//   lines.
	tagsRemoved := []byte{}
	offsets := []int{}
	escapedQuery := []byte(html.EscapeString(query))
	for len(buf) > 0 {
		// remove any html tags, keeping track of the original offsets of each
		// character.  also look for the terminating \n within the same loop.
		tagsRemoved = tagsRemoved[:0]
		offsets = offsets[:0]
		offset := 0
		i := 0
		for ; i+offset < len(buf); i++ {
			offsets = append(offsets, offset)
			for i+offset < len(buf) && buf[i+offset] == '<' {
				tagLength := 0
				// if it's crashing here, there's an unmatched angle bracket.
				for buf[i+offset+tagLength] != '>' {
					tagLength++
				}
				tagLength++
				offset += tagLength
			}
			if i+offset < len(buf) {
				b := buf[i+offset]
				tagsRemoved = append(tagsRemoved, b)
				if b == '\n' {
					i++
					break
				}
			}
		}
		offsets = append(offsets, offset)
		line := buf[:i+offset]
		buf = buf[i+offset:]
		// search for the query string in the text with all tags removed.
		index := bytes.Index(tagsRemoved, escapedQuery)
		lastEnd := 0
		var result searchResultLine
		for index >= 0 {
			// match!  find the original offsets and insert begin/end tags.
			result.lineType = lineTypeMatch
			// this is kind of confusing; index and endIndex refer to positions
			// in the tagsRemoved slice.  start and end refer to positions in
			// the line slice.  the offsets slice is used to translate between
			// the two.
			endIndex := index + len(escapedQuery)
			start := index + offsets[index]
			end := endIndex + offsets[endIndex]
			startTag, endTag := tags()
			if lastEnd == 0 {
				result.bytes = []byte{}
			}
			result.bytes = append(result.bytes, line[lastEnd:start]...)
			result.bytes = append(result.bytes, []byte(startTag)...)
			result.bytes = append(result.bytes, line[start:end]...)
			result.bytes = append(result.bytes, []byte(endTag)...)
			lastEnd = end
			index = bytes.Index(tagsRemoved[endIndex:], escapedQuery)
			if index >= 0 {
				index += endIndex
			}
		}
		if lastEnd > 0 {
			result.bytes = append(result.bytes, line[lastEnd:]...)
		} else {
			result.bytes = line
		}
		visit(result)
	}
	visit(searchResultLine{lineTypeSurrounding, after})
}

func (c *cache) search(ar *archive, query string, filter string, results chan searchResult) {
	defer close(results)
	defer func() {
		if r := recover(); r != nil {
			log.Print("recovered in search: ", r, "\n", string(debug.Stack()))
			results <- searchResult{err: fmt.Errorf("panic during search: %v\n%v", r, string(debug.Stack()))}
		}
	}()
	if len(query) == 0 {
		return
	}
	filterRegexp, err := regexp.Compile(filter)
	if err != nil {
		results <- searchResult{err: err}
		return
	}
	// startTime := time.Now()
	timeout := time.After(searchTimeoutSeconds * time.Second)
	searched := 0
	globalMatch := 0
	filenames, err := ar.searchIndex.search([]byte(query))
	if err != nil {
		results <- searchResult{err: fmt.Errorf("there's a problem with the search index: %s", err.Error())}
		log.Print(err)
		return
	}
	for _, filename := range filenames {
		if globalMatch >= searchResultLimit {
			results <- searchResult{err: fmt.Errorf("to avoid melting your browser, only the first %d matches have been returned.", searchResultLimit)}
			return
		}
		if filterRegexp != nil && !filterRegexp.MatchString(filename) {
			continue
		}
		ar.mutex.Lock()
		rendered := ar.requestRender(filename)
		ar.mutex.Unlock()
		select {
		case <-timeout:
			results <- searchResult{err: fmt.Errorf("stopped searching after %d seconds.  this includes waiting for files to be indexed, so reloading the page may help.", searchTimeoutSeconds)}
			return
		case <-rendered:
			// check for a "text" version of the file first (in case it's
			// rendered as markdown).
			buf, err := ioutil.ReadFile(path.Join(c.textPath, ar.path, filename))
			if err != nil {
				buf, err = ioutil.ReadFile(path.Join(c.rootPath, ar.path, filename))
			}
			if err != nil {
				results <- searchResult{err: fmt.Errorf("unable to open file \u201C%s\u201D", filename)}
				continue
			}
			searched++
			whichMatch := 0
			tags := func() (string, string) {
				whichMatch++
				globalMatch++
				return searchResultTags(filename, query, whichMatch, globalMatch)
			}
			matchLine := -1
			lineNumber := 1
			var lines [][]byte
			matchLines(buf, query, tags, func(line searchResultLine) {
				if line.lineType == lineTypeSurrounding {
					return
				} else if line.lineType == lineTypeMatch {
					matchLine = len(lines)
				}
				lineNumber++
				lines = append(lines, line.bytes)
				if matchLine >= 0 && len(lines) > matchLine+matchContextLinesAfter+matchContextLinesBefore+1 {
					numLines := matchLine + matchContextLinesAfter + 1
					results <- searchResult{
						file:      filename,
						firstLine: lineNumber - len(lines),
						lines:     numLines,
						html:      string(bytes.Join(lines[:numLines], nil)),
					}
					lines = lines[numLines:]
					matchLine = -1
				}
				if matchLine < 0 && len(lines) > matchContextLinesBefore {
					copy(lines[:matchContextLinesBefore], lines[len(lines)-matchContextLinesBefore:])
					lines = lines[:matchContextLinesBefore]
				}
			})
			if matchLine >= 0 {
				numLines := matchLine + matchContextLinesAfter + 1
				if numLines > len(lines) {
					numLines = len(lines)
				}
				results <- searchResult{
					file:      filename,
					firstLine: lineNumber - len(lines),
					lines:     numLines,
					html:      string(bytes.Join(lines[:numLines], nil)),
				}
			}
		}
	}
	// results <- searchResult{ err: fmt.Errorf("searched files: %d; elapsed time: %v", searched, time.Now().Sub(startTime)) }
}

func insertSearchAnchors(w io.Writer, r io.Reader, query string) error {
	var b bytes.Buffer
	_, err := b.ReadFrom(r)
	if err != nil {
		return err
	}
	whichMatch := 0
	matchLines(b.Bytes(), query, func() (string, string) {
		whichMatch++
		return searchAnchorTags(whichMatch)
	}, func(line searchResultLine) {
		if err != nil {
			return
		}
		_, err = w.Write(line.bytes)
	})
	return err
}

// -- rabin-karp rolling hash

const rabinKarpBase uint32 = 16777619

type rabinKarp struct {
	outgoingBase uint32
	bytes        []byte
	windowSize   int

	hash           uint32
	outgoingOffset int
}

func newRabinKarp(bytes []byte, windowSize int) *rabinKarp {
	rk := &rabinKarp{outgoingBase: 1, bytes: bytes, windowSize: windowSize, outgoingOffset: -1}
	for i := 0; i < windowSize && i < len(bytes); i++ {
		rk.outgoingBase *= rabinKarpBase
		rk.step(bytes[i], 0)
	}
	return rk
}
func (rk *rabinKarp) step(incoming byte, outgoing byte) {
	rk.hash = rk.hash*rabinKarpBase + uint32(incoming) - rk.outgoingBase*uint32(outgoing)
}
func (rk *rabinKarp) next() bool {
	if rk.outgoingOffset+rk.windowSize >= len(rk.bytes) {
		return false
	} else if rk.outgoingOffset >= 0 {
		incomingOffset := rk.outgoingOffset + rk.windowSize
		rk.step(rk.bytes[incomingOffset], rk.bytes[rk.outgoingOffset])
	}
	rk.outgoingOffset++
	return true
}

// -- search index

const filterBits = 14
const filterSize = 1 << filterBits
const filterMask = filterSize - 1
const filterMix = 2166136261 // this is the fnv 32-bit offset basis
type searchIndex struct {
	file          *os.File
	contents      []byte
	numberOfFiles int

	nextFileIndex int
}

const PROT_READ = 0x1
const PROT_WRITE = 0x2
const MAP_SHARED = 0x1

func createSearchIndex(filename string, numberOfFiles int) (*searchIndex, error) {
	idx := &searchIndex{numberOfFiles: numberOfFiles}
	var err error
	idx.file, err = os.Create(filename)
	if err != nil {
		return nil, err
	}
	if err = idx.file.Truncate(int64(idx.filenamesOffset())); err != nil {
		idx.file.Close()
		return nil, err
	}
	if _, err = idx.file.Seek(0, io.SeekEnd); err != nil {
		idx.file.Close()
		return nil, err
	}
	idx.contents, err = syscall.Mmap(int(idx.file.Fd()), 0, idx.filenamesOffset(), PROT_READ|PROT_WRITE, MAP_SHARED)
	if err != nil {
		idx.file.Close()
		return nil, err
	}
	return idx, nil
}
func openSearchIndex(filename string, numberOfFiles int) (*searchIndex, error) {
	idx := &searchIndex{numberOfFiles: numberOfFiles}
	var err error
	idx.file, err = os.Open(filename)
	if err != nil {
		return nil, err
	}
	idx.contents, err = syscall.Mmap(int(idx.file.Fd()), 0, idx.filenamesOffset(), PROT_READ, MAP_SHARED)
	if err != nil {
		idx.file.Close()
		return nil, err
	}
	return idx, nil
}
func (idx *searchIndex) close() {
	idx.file.Close()
	syscall.Munmap(idx.contents)
}
func (idx *searchIndex) filterStride() int {
	return (idx.numberOfFiles + 7) / 8
}
func (idx *searchIndex) filenamesOffset() int {
	return idx.filterStride()*filterSize + idx.numberOfFiles
}
func (idx *searchIndex) trigramFilter() []byte {
	return idx.contents[:idx.filterStride()*filterSize]
}
func (idx *searchIndex) filenameLengths() []byte {
	return idx.contents[idx.filterStride()*filterSize : idx.filenamesOffset()]
}
func (idx *searchIndex) addFile(name string, contents []byte) {
	if len(name) == 0 {
		log.Print("searchIndex.addFile(): ignoring file with empty name")
		return
	} else if len(name) > 0xff {
		log.Printf("searchIndex.addFile(): ignoring file '%.9s...' - name too long to index", name)
		return
	}
	rk := newRabinKarp(contents, 3)
	filter := idx.trigramFilter()
	stride := idx.filterStride()
	index := idx.nextFileIndex
	idx.nextFileIndex++
	for rk.next() {
		h := rk.hash * filterMix
		filter[stride*int(h&filterMask)+index/8] |= 1 << (index % 8)
		filter[stride*int((h>>filterBits)&filterMask)+index/8] |= 1 << (index % 8)
	}
	idx.filenameLengths()[index] = byte(len(name))
	io.WriteString(idx.file, name)
}
func (idx *searchIndex) search(query []byte) ([]string, error) {
	rk := newRabinKarp(query, 3)
	filter := idx.trigramFilter()
	stride := idx.filterStride()
	matches := bytes.Repeat([]byte{0xff}, stride)
	filenames := []string{}
	for rk.next() {
		h := rk.hash * filterMix
		for i := 0; i < stride; i++ {
			matches[i] &= filter[stride*int(h&filterMask)+i]
			matches[i] &= filter[stride*int((h>>filterBits)&filterMask)+i]
		}
	}
	nameLengths := idx.filenameLengths()
	nameOffset := int64(idx.filenamesOffset())
	for i := 0; i < idx.numberOfFiles; i++ {
		n := int(nameLengths[i])
		if n == 0 {
			break
		}
		offset := nameOffset
		nameOffset += int64(n)
		if matches[i/8]&(1<<(i%8)) == 0 {
			continue
		}
		buf := make([]byte, n)
		if m, err := idx.file.ReadAt(buf, offset); n != m {
			return nil, err
		}
		filenames = append(filenames, string(buf))
	}
	return filenames, nil
}
