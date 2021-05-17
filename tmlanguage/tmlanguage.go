// tmlanguage.go
// author: ian henderson <ian@ianhenderson.org>

package tm

// #cgo pkg-config: oniguruma
// #include "tmlanguage.h"
// #include <stdlib.h>
// void freeString(char *s) { free(s); }
import "C"

import (
    "errors"
    "fmt"
    "path"
    "runtime"
    "strings"
    "unsafe"
)

// see https://macromates.com/manual/en/language_grammars for documentation
// about the values in these structs.
type Language struct {
    ScopeName string `plist:"scopeName"`
    FileTypes []string `plist:"fileTypes"`
    Patterns []*Rule `plist:"patterns"`
    FirstLineMatch string `plist:"firstLineMatch"`
    Repository map[string]*Rule `plist:"repository"`
}
type Rule struct {
    Name string `plist:"name"`
    ContentName string `plist:"contentName"`

    Match string `plist:"match"`
    Begin string `plist:"begin"`
    End string `plist:"end"`
    While string `plist:"while"`

    Patterns []*Rule `plist:"patterns"`
    Repository map[string]*Rule `plist:"repository"`

    Captures map[string]Capture `plist:"captures"`
    BeginCaptures map[string]Capture `plist:"beginCaptures"`
    EndCaptures map[string]Capture `plist:"endCaptures"`
    WhileCaptures map[string]Capture `plist:"whileCaptures"`

    Include string `plist:"include"`

    Disabled int `plist:"disabled"`
    ApplyEndPatternLast int `plist:"applyEndPatternLast"`
}
type Capture struct {
    Name string `plist:"name"`
    Patterns []*Rule `plist:"patterns"`
    Repository map[string]*Rule `plist:"repository"`
}

type Highlighter struct {
    languages []*Language
    languagesByScopeName map[string]*Language
    languagesByFileExtension map[string]*Language
    scopeData func(string)interface{}
    scopeId map[string]int
    scopeDataForId []interface{}

    startState map[*Language]*C.state
    firstLineMatch map[*Language]*C.pattern
    ruleMatch map[*Rule]*C.pattern
    ruleBegin map[*Rule]*C.pattern
    ruleEnd map[*Rule]*C.pattern
    ruleWhile map[*Rule]*C.pattern
    ruleState map[*Rule]*C.state
    ruleRepository map[*Rule]func(string)*Rule
    deferredStates map[*Language][]deferredState
}

type deferredState struct {
    state *C.state
    patterns []*Rule
}

func NewHighlighter(languages []*Language, scopeData func(string)interface{}) (*Highlighter, error) {
    C.initialize()
    h := &Highlighter{
        languages: languages,
        languagesByScopeName: map[string]*Language{},
        languagesByFileExtension: map[string]*Language{},
        scopeData: scopeData,
        scopeId: map[string]int{},
        scopeDataForId: []interface{}{ nil },

        startState: map[*Language]*C.state{},
        firstLineMatch: map[*Language]*C.pattern{},
        ruleMatch: map[*Rule]*C.pattern{},
        ruleBegin: map[*Rule]*C.pattern{},
        ruleEnd: map[*Rule]*C.pattern{},
        ruleWhile: map[*Rule]*C.pattern{},
        ruleState: map[*Rule]*C.state{},
        ruleRepository: map[*Rule]func(string)*Rule{},
        deferredStates: map[*Language][]deferredState{},
    }
    runtime.SetFinalizer(h, freeHighlighterData)
    for _, lang := range languages {
        if _, ok := h.languagesByScopeName[lang.ScopeName]; ok {
            return nil, fmt.Errorf("tm.NewHighlighter(): two languages share the scope name %s", lang.ScopeName)
        }
        h.languagesByScopeName[lang.ScopeName] = lang
        for _, v := range lang.FileTypes {
            // if other, ok := h.languagesByFileExtension[v]; ok {
            //     fmt.Printf("tm.NewHighlighter(): both %s and %s want to use file extension '%s'\n", lang.ScopeName, other.ScopeName, v)
            // }
            h.languagesByFileExtension[v] = lang
        }
        if lang.FirstLineMatch != "" {
            var errmsg *C.char
            pattern := C.createPattern(&([]C.uchar(lang.FirstLineMatch))[0], C.size_t(len(lang.FirstLineMatch)), &errmsg)
            if errmsg != nil {
                err := errors.New(C.GoString(errmsg))
                C.freeString(errmsg)
                return nil, err
            }
            h.firstLineMatch[lang] = pattern
        }
        for _, v := range lang.Patterns {
            if err := h.createPatterns(lang, v); err != nil {
                return nil, err
            }
        }
        for _, v := range lang.Repository {
            if err := h.createPatterns(lang, v); err != nil {
                return nil, err
            }
        }
    }
    for _, lang := range languages {
        // lang changes during the loop so we have to make a new binding here...
        l := lang
        rootRepoFunc := func (s string) *Rule { return l.Repository[s] }
        for _, v := range lang.Patterns {
            h.linkRepositories(v, rootRepoFunc)
        }
        for _, v := range lang.Repository {
            h.linkRepositories(v, rootRepoFunc)
        }
    }
    for _, lang := range languages {
        h.startState[lang] = C.createState()
        h.addToState(h.startState[lang], lang, lang, lang.Patterns)
        for _, v := range h.deferredStates[lang] {
            h.addToState(v.state, lang, lang, v.patterns)
            v.patterns = nil
        }
    }
    return h, nil
}

func (h *Highlighter) createPatterns(lang *Language, r *Rule) error {
    if r.Disabled != 0 {
        return nil
    }
    var err error
    if len(r.Match) > 0 {
        if h.ruleMatch[r], err = h.createPattern(lang, r.Match, r.Name, "", "", r.Captures, nil, false); err != nil {
            return err
        }
    }
    if len(r.Begin) > 0 {
        if h.ruleBegin[r], err = h.createPattern(lang, r.Begin, "", r.ContentName, r.Name, r.Captures, r.BeginCaptures, false); err != nil {
            return err
        }
    }
    if len(r.End) > 0 {
        if h.ruleEnd[r], err = h.createPattern(lang, r.End, "", r.ContentName, r.Name, r.Captures, r.EndCaptures, true); err != nil {
            return err
        }
    }
    if len(r.While) > 0 {
        if h.ruleWhile[r], err = h.createPattern(lang, r.While, "", r.ContentName, r.Name, r.Captures, r.WhileCaptures, true); err != nil {
            return err
        }
    }
    for _, v := range r.Patterns {
        if err := h.createPatterns(lang, v); err != nil {
            return err
        }
    }
    for _, v := range r.Repository {
        if err := h.createPatterns(lang, v); err != nil {
            return err
        }
    }
    return nil
}

func (h *Highlighter) createPattern(lang *Language, match string, name string, innerName string, outerName string, generalCaptures map[string]Capture, specificCaptures map[string]Capture, backreferencing bool) (*C.pattern, error) {
    var errmsg *C.char
    var pattern *C.pattern
    if backreferencing {
        pattern = C.createBackreferencingPattern(&([]C.uchar(match))[0], C.size_t(len(match)), &errmsg)
    } else {
        pattern = C.createPattern(&([]C.uchar(match))[0], C.size_t(len(match)), &errmsg)
    }
    if errmsg != nil {
        err := errors.New(C.GoString(errmsg))
        C.freeString(errmsg)
        return nil, err
    }
    if len(name) > 0 {
        C.setCaptureScope(pattern, &([]C.uchar("0"))[0], 1, C.int(h.getScopeId(name)))
    }
    if len(innerName) > 0 {
        C.setInnerScope(pattern, C.int(h.getScopeId(innerName)))
    }
    if len(outerName) > 0 {
        C.setOuterScope(pattern, C.int(h.getScopeId(outerName)))
    }
    captures := map[string]Capture{}
    for k, v := range generalCaptures { captures[k] = v }
    for k, v := range specificCaptures { captures[k] = v }
    for k, v := range captures {
        if len(v.Name) > 0 {
            C.setCaptureScope(pattern, &([]C.uchar(k))[0], C.size_t(len(k)), C.int(h.getScopeId(v.Name)))
        }
        for _, p := range v.Patterns {
            if err := h.createPatterns(lang, p); err != nil {
                return nil, err
            }
            state := C.createState()
            C.setCaptureState(pattern, &([]C.uchar(k))[0], C.size_t(len(k)), state)
            h.deferredStates[lang] = append(h.deferredStates[lang], deferredState{ state, v.Patterns })
        }
        for _, p := range v.Repository {
            if err := h.createPatterns(lang, p); err != nil {
                return nil, err
            }
        }
    }
    return pattern, nil
}

func (h *Highlighter) linkRepositories(r *Rule, outerRepoFunc func(string)*Rule) {
    repoFunc := outerRepoFunc
    if len(r.Repository) > 0 {
        repoFunc = func (s string) *Rule {
            rule := r.Repository[s]
            if rule != nil {
                return rule
            } else {
                return outerRepoFunc(s)
            }
        }
    }
    h.ruleRepository[r] = repoFunc
    for _, v := range r.Patterns {
        h.linkRepositories(v, repoFunc)
    }
    for _, v := range r.Repository {
        h.linkRepositories(v, repoFunc)
    }
    for _, c := range r.Captures {
        h.linkCaptureRepositories(c, repoFunc)
    }
    for _, c := range r.BeginCaptures {
        h.linkCaptureRepositories(c, repoFunc)
    }
    for _, c := range r.EndCaptures {
        h.linkCaptureRepositories(c, repoFunc)
    }
    for _, c := range r.WhileCaptures {
        h.linkCaptureRepositories(c, repoFunc)
    }
}

func (h *Highlighter) linkCaptureRepositories(c Capture, outerRepoFunc func(string)*Rule) {
    repoFunc := outerRepoFunc
    if len(c.Repository) > 0 {
        repoFunc = func (s string) *Rule {
            rule := c.Repository[s]
            if rule != nil {
                return rule
            } else {
                return outerRepoFunc(s)
            }
        }
    }
    for _, v := range c.Patterns {
        h.linkRepositories(v, repoFunc)
    }
    for _, v := range c.Repository {
        h.linkRepositories(v, repoFunc)
    }
}

func (h *Highlighter) addToState(s *C.state, lang *Language, base *Language, rules []*Rule) {
    for _, rule := range rules {
        if rule.Disabled != 0 {
            continue
        }
        if len(rule.Include) > 0 {
            if rule.Include == "$self" {
                h.addToState(s, lang, base, lang.Patterns)
            } else if rule.Include == "$base" {
                h.addToState(s, base, base, base.Patterns)
            } else if strings.HasPrefix(rule.Include, "#") {
                if r := h.ruleRepository[rule](rule.Include[1:]); r != nil {
                    h.addToState(s, lang, base, []*Rule{r})
                }
            } else if n := strings.Index(rule.Include, "#"); n > 0 {
                if extlang := h.languagesByScopeName[rule.Include[:n]]; extlang != nil && extlang.Repository != nil {
                    if r := extlang.Repository[rule.Include[n+1:]]; r != nil {
                        h.addToState(s, extlang, base, []*Rule{r})
                    }
                }
            } else if extlang := h.languagesByScopeName[rule.Include]; extlang != nil {
                h.addToState(s, extlang, base, extlang.Patterns)
            }
        } else if h.ruleMatch[rule] != nil {
            C.addMatch(s, h.ruleMatch[rule])
        } else if h.ruleBegin[rule] != nil {
            ruleState := h.ruleState[rule]
            if ruleState == nil {
                ruleState = C.createState()
                h.ruleState[rule] = ruleState
                // fmt.Printf("[state %p for %v]\n", ruleState, rule)
                if h.ruleWhile[rule] != nil {
                    C.setWhile(ruleState, h.ruleWhile[rule])
                }
                if h.ruleEnd[rule] != nil {
                    C.setEnd(ruleState, h.ruleEnd[rule], C._Bool(rule.ApplyEndPatternLast != 0))
                }
                // note that the base is set to lang here in the recursive call
                // -- just because we discovered this rule while analyzing a
                // different language doesn't mean that other language is the
                // base for the patterns this rule contains.
                h.addToState(ruleState, lang, lang, rule.Patterns)
            }
            C.addBegin(s, ruleState, h.ruleBegin[rule])
        } else if len(rule.Patterns) > 0 {
            h.addToState(s, lang, base, rule.Patterns)
        }
    }
}

func (h *Highlighter) getScopeId(scopeName string) int {
    id, ok := h.scopeId[scopeName]
    if ok {
        return id
    }
    data := h.scopeData(scopeName)
    if data != nil {
        id = len(h.scopeDataForId)
        h.scopeDataForId = append(h.scopeDataForId, data)
    } else {
        // if ok is false, then id must be zero, but set it again for clarity's
        // sake.
        id = 0
    }
    h.scopeId[scopeName] = id
    return id
}

func freeHighlighterData(h *Highlighter) {
    for _, v := range h.startState {
        C.freeState(v)
    }
    for _, v := range h.firstLineMatch {
        C.freePattern(v)
    }
    for _, v := range h.ruleMatch {
        C.freePattern(v)
    }
    for _, v := range h.ruleBegin {
        C.freePattern(v)
    }
    for _, v := range h.ruleEnd {
        C.freePattern(v)
    }
    for _, v := range h.ruleWhile {
        C.freePattern(v)
    }
    for _, v := range h.ruleState {
        C.freeState(v)
    }
    for _, deferred := range h.deferredStates {
        for _, v := range deferred {
            C.freeState(v.state)
        }
    }
}

type Writer interface {
    Write([]byte) (int, error)
    BeginScope(interface{}) error
    EndScope(interface{}) error
    NewLine() error
}

func (h *Highlighter) Highlight(w Writer, fileData []byte, fileName string) error {
    if len(fileData) == 0 {
        // &ucharData[0] will panic if the fileData is empty.
        return nil
    }
    var lang *Language
    ext := path.Ext(fileName)
    if len(ext) > 0 {
        lang = h.languagesByFileExtension[ext[1:]]
    } else {
        // this is used for things like makefiles.
        lang = h.languagesByFileExtension[path.Base(fileName)]
    }
    ucharData := []C.uchar(string(fileData))
    if lang == nil {
        for _, l := range h.languages {
            p := h.firstLineMatch[l]
            if p != nil && C.firstLineMatch(&ucharData[0], C.size_t(len(ucharData)), p) {
                // fmt.Printf("%s has matching first line for %s\n", fileName, l.ScopeName)
                lang = l
                break
            }
        }
    // } else {
    //     fmt.Printf("%s has matching extension for %s\n", fileName, lang.ScopeName)
    }
    r := C.createRenderer(&ucharData[0], C.size_t(len(ucharData)), h.startState[lang])
    defer C.freeRenderer(r)
    line := C.line{}
    defer C.freeLine(line)
    for C.renderNextLine(r, &line) {
        offset := line.begin
        for i := C.ulong(0); i < line.scopesLength; i++ {
            scope := (*C.scope)(unsafe.Pointer(uintptr(unsafe.Pointer(line.scopes)) + uintptr(i * C.sizeof_scope)))
            if scope.offset > offset {
                if _, err := w.Write(fileData[offset:scope.offset]); err != nil {
                    return err
                }
                offset = scope.offset
            }
            if scope.ty == C.SCOPE_BEGIN {
                if err := w.BeginScope(h.scopeDataForId[int(scope.name)]); err != nil {
                    return err
                }
            } else if scope.ty == C.SCOPE_END {
                if err := w.EndScope(h.scopeDataForId[int(scope.name)]); err != nil {
                    return err
                }
            }
        }
        if offset < line.end {
            if _, err := w.Write(fileData[offset:line.end]); err != nil {
                return err
            }
        }
        if err := w.NewLine(); err != nil {
            return err
        }
    }
    return nil
}
