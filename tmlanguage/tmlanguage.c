// tmlanguage.c
// author: ian henderson <ian@ianhenderson.org>

#include "tmlanguage.h"

#include <assert.h>
#include <limits.h>
#include <oniguruma.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

// #define LOG

void initialize(void)
{
    onig_initialize((OnigEncodingType *[]){ ONIG_ENCODING_UTF8 }, 1);
}

struct pattern {
    OnigRegex re;
    scopeName innerScope;
    scopeName outerScope;
    int captures;
    scopeName *captureScopes;
    state **captureStates;

    // used for backreferencing end/while patterns.
    bool backreferencingPattern;
    unsigned char *text;
    size_t len;
};

pattern *createPattern(const unsigned char *regex, size_t len,
 char **error)
{
    pattern *p = calloc(1, sizeof(pattern));
    OnigErrorInfo errInfo;
    int res = onig_new(&p->re, regex, regex + len, ONIG_OPTION_CAPTURE_GROUP,
     ONIG_ENCODING_UTF8, ONIG_SYNTAX_ONIGURUMA, &errInfo);
    if (res != ONIG_NORMAL) {
        if (!error) {
            freePattern(p);
            return 0;
        }
        char *err = malloc(ONIG_MAX_ERROR_MESSAGE_LEN);
        int errlen = onig_error_code_to_str((UChar *)err, res, &errInfo);
        freePattern(p);
        const char *fmt = "onig_new(): %.*s in pattern %.*s";
        int n = snprintf(0, 0, fmt, errlen, err, (int)len, (char *)regex);
        if (n < 0 || n == INT_MAX) {
            free(err);
            return 0;
        }
        *error = malloc(n + 1);
        snprintf(*error, n + 1, fmt, errlen, err, (int)len, (char *)regex);
        free(err);
        return 0;
    }
    p->captures = onig_number_of_captures(p->re) + 1;
#ifdef LOG
    fprintf(stderr, "[created '%.*s' %p (%p) - %d captures]\n", len, regex, p->re, p, p->captures);
#endif
    p->captureScopes = calloc(p->captures, sizeof(int));
    p->captureStates = calloc(p->captures, sizeof(state *));
    return p;
}

pattern *createBackreferencingPattern(const unsigned char *regex,
 size_t len, char **error)
{
    unsigned char *text = malloc(len);
    memcpy(text, regex, len);
    // take the backreferences out so the regex compiles and the captures can
    // be counted.
    bool backreferencingPattern = false;
    for (size_t i = 0; i + 1 < len; ++i) {
        if (text[i] == '\\' && (text[i+1] >= '0' && text[i+1] <= '9')) {
            backreferencingPattern = true;
            text[i] = '0';
        }
    }
    pattern *p = createPattern(text, len, error);
    if (!p) {
        free(text);
        return 0;
    }
    if (!backreferencingPattern) {
        free(text);
        return p;
    }
    p->backreferencingPattern = backreferencingPattern;
    p->text = text;
    p->len = len;
    // put the backslashes back in.
    memcpy(p->text, regex, len);
    return p;
}

void setInnerScope(pattern *p, scopeName name)
{
    p->innerScope = name;
}

void setOuterScope(pattern *p, scopeName name)
{
    p->outerScope = name;
}

static int captureNameToInt(const unsigned char *captureName, size_t len)
{
    uint32_t result = 0;
    for (size_t i = 0; i < len; ++i) {
        result *= 10;
        if (captureName[i] < '0' || captureName[i] > '9')
            return -1;
        result += captureName[i] - '0';
    }
    if (result > INT_MAX)
        return -1;
    return (int)result;
}

void setCaptureScope(pattern *p, const unsigned char *name, size_t len, scopeName scope)
{
    int capture = captureNameToInt(name, len);
    if (capture >= 0 && capture < p->captures) {
        p->captureScopes[capture] = scope;
        return;
    }
    int *captures = 0;
    int n = onig_name_to_group_numbers(p->re, name, name + len, &captures);
    if (n < 0)
        return;
    for (int i = 0; i < n; ++i)
        p->captureScopes[captures[i]] = scope;
}

void setCaptureState(pattern *p, const unsigned char *name, size_t len, state *s)
{
    int capture = captureNameToInt(name, len);
    if (capture >= 0 && capture < p->captures) {
        p->captureStates[capture] = s;
        return;
    }
    int *captures = 0;
    int n = onig_name_to_group_numbers(p->re, name, name + len, &captures);
    if (n < 0)
        return;
    for (int i = 0; i < n; ++i)
        p->captureStates[captures[i]] = s;
}

void freePattern(pattern *p)
{
    if (!p)
        return;
    onig_free(p->re);
    free(p->captureScopes);
    free(p->captureStates);
    free(p->text);
    free(p);
}

typedef struct patternInState {
    enum { MATCH, BEGIN } type;
    pattern *p;
    state *to; // for begin patterns
} patternInState;

struct state {
    OnigRegSet *regset;
    pattern *whilePattern;
    pattern *endPattern;
    bool applyEndPatternLast;
    patternInState *patterns;
    size_t patternsCapacity;
    size_t patternsLength;
};

static void addPatternInState(state *s, patternInState p)
{
    if (!s)
        return;
    size_t cap = s->patternsCapacity;
    if (cap == s->patternsLength) {
        if (cap == 0)
            cap = 16;
        else
            cap *= 2;
        if (cap == 0) {
            fprintf(stderr, "fail: overflow\n");
            return;
        }
        s->patterns = realloc(s->patterns, cap * sizeof(patternInState));
        s->patternsCapacity = cap;
    }
    int res = onig_regset_add(s->regset, p.p->re);
    if (res != ONIG_NORMAL) {
        fprintf(stderr, "fail: onig_regset_add()\n");
        return;
    }
    s->patterns[s->patternsLength++] = p;
    assert(s->patternsLength == onig_regset_number_of_regex(s->regset));
}

state *createState(void)
{
    state *s = calloc(1, sizeof(state));
    int res = onig_regset_new(&s->regset, 0, 0);
    if (res != ONIG_NORMAL) {
        // the only possible error is malloc failure, e.g. ONIGERR_MEMORY.
        fprintf(stderr, "out of memory in onig_regset_new()\n");
        onig_regset_free(s->regset);
        free(s);
        return 0;
    }
    return s;
}

void addMatch(state *s, pattern *match)
{
    addPatternInState(s, (patternInState){ MATCH, match });
}

void addBegin(state *s, state *to, pattern *begin)
{
    addPatternInState(s, (patternInState){ BEGIN, begin, to });
}

void setEnd(state *s, pattern *end, bool applyLast)
{
    s->endPattern = end;
    s->applyEndPatternLast = applyLast;
}

void setWhile(state *s, pattern *while_)
{
    s->whilePattern = while_;
}

void freeState(state *s)
{
    if (!s)
        return;
    for (int i = 0; i < onig_regset_number_of_regex(s->regset); ++i) {
        // zero out the regexes here so they aren't freed by onig_regset_free.
        onig_regset_replace(s->regset, i, 0);
    }
    onig_regset_free(s->regset);
    free(s->patterns);
    free(s);
}

typedef struct activeState {
    state *s;
    pattern *p;
    OnigRegion *beginRegion;
    OnigRegex endRegex;
    OnigRegex whileRegex;
    size_t beginOffset;
    size_t outerBegin;
    size_t outerSeq;
    size_t innerBegin; // also used to enable/disable \G matches.
    size_t innerSeq;
} activeState;

struct renderer {
    const unsigned char *bytes;
    size_t length;
    size_t offset;

    activeState stack[256];
    size_t stackDepth;

    size_t seq;
};

renderer *createRenderer(const unsigned char *bytes, size_t len, state *startState)
{
    renderer *r = calloc(1, sizeof(renderer));
    r->bytes = bytes;
    r->length = len;
    r->stack[0].s = startState;
    r->stackDepth = 1;
    return r;
}

static void popStack(renderer *r, size_t depth)
{
    while (r->stackDepth > depth) {
        onig_free(r->stack[r->stackDepth - 1].endRegex);
        onig_free(r->stack[r->stackDepth - 1].whileRegex);
        onig_region_free(r->stack[r->stackDepth - 1].beginRegion, 1);
        r->stackDepth--;
    }
}

void freeRenderer(renderer *r)
{
    popStack(r, 0);
    free(r);
}

static size_t advanceToNextLine(const unsigned char *bytes, size_t len, size_t *offset)
{
    while (true) {
        size_t beforeNewline = *offset;
        if (*offset >= len)
            return beforeNewline;
        if (bytes[*offset] == '\n') {
            (*offset)++;
            return beforeNewline;
        } else if (bytes[*offset] == '\r') {
            (*offset)++;
            if (*offset < len && bytes[*offset] == '\n')
                (*offset)++;
            return beforeNewline;
        }
        (*offset)++;
    }
}

bool firstLineMatch(const unsigned char *bytes, size_t len, pattern* p)
{
    size_t offset = 0;
    advanceToNextLine(bytes, len, &offset);
    return onig_match(p->re, bytes, bytes + offset, bytes, 0, 0) >= 0;
}

static void addScope(line *line, scope s)
{
    if (line->scopesCapacity == line->scopesLength) {
        if (line->scopesCapacity == 0)
            line->scopesCapacity = 8;
        else
            line->scopesCapacity *= 2;
        if (line->scopesCapacity == 0) {
            fprintf(stderr, "addScope(): overflow\n");
            return;
        }
        line->scopes = realloc(line->scopes, line->scopesCapacity * sizeof(scope));
    }
    line->scopes[line->scopesLength++] = s;
}

static void addScopeRange(line *line, scopeName name, size_t seq, size_t begin, size_t end)
{
    size_t clampedBegin = begin < line->begin ? line->begin : begin;
    size_t clampedEnd = end > line->end ? line->end : end;
    if (name == 0 || clampedBegin >= clampedEnd)
        return;
    addScope(line, (scope){
        .ty = SCOPE_BEGIN,
        .name = name,
        .offset = clampedBegin,
        .startOffset = begin,
        .endOffset = end,
        .seq = seq,
    });
    addScope(line, (scope){
        .ty = SCOPE_END,
        .name = name,
        .offset = clampedEnd,
        .startOffset = begin,
        .endOffset = end,
        .seq = seq,
    });
}

static int backreferencingSearch(renderer *r, activeState a, OnigRegex *re,
 pattern *p, const UChar* str, const UChar* end, const UChar* start,
 const UChar* range, OnigRegion* region, OnigOptionType option)
{
    if (!p->backreferencingPattern)
        return onig_search(p->re, str, end, start, range, region, option);
    if (*re)
        return onig_search(*re, str, end, start, range, region, option);
    if (!a.beginRegion)
        return -1;
    if (p->len > SIZE_MAX / 2)
        return -1;
    size_t size = p->len * 2;
    size_t offset = 0;
    unsigned char *replaced = malloc(size);
    for (size_t i = 0; i < p->len; ++i) {
        unsigned char *text = p->text;
        size_t appendStart = i;
        size_t appendEnd = i + 1;
        bool escape = false;
        if (i+1 < p->len && p->text[i] == '\\' && p->text[i+1] >= '0' && p->text[i+1] <= '9') {
            i++;
            size_t which = p->text[i] - '0';
            if (which >= a.beginRegion->num_regs ||
             a.beginRegion->beg[which] < 0 ||
             a.beginRegion->end[which] < a.beginRegion->beg[which])
                goto fail;
            size_t start = a.beginOffset + a.beginRegion->beg[which];
            size_t end = a.beginOffset + a.beginRegion->end[which];
            size_t reserved = offset + 4 * (end - start) + p->len - i;
            if (reserved > size) {
                while (reserved > size) {
                    if (size > SIZE_MAX / 2)
                        goto fail;
                    size *= 2;
                }
                unsigned char *r = realloc(replaced, size);
                if (!r)
                    goto fail;
                replaced = r;
            }
            for (size_t j = start; j < end; ++j) {
                replaced[offset++] = '\\';
                replaced[offset++] = '0' + ((r->bytes[j] & 0700) >> 6);
                replaced[offset++] = '0' + ((r->bytes[j] & 0070) >> 3);
                replaced[offset++] = '0' + ((r->bytes[j] & 0007) >> 0);
            }
        } else
            replaced[offset++] = p->text[i];
    }
    int res = onig_new(re, replaced, replaced + offset,
     ONIG_OPTION_CAPTURE_GROUP, ONIG_ENCODING_UTF8, ONIG_SYNTAX_ONIGURUMA, 0);
    if (res < 0)
        goto fail;
    res = onig_search(*re, str, end, start, range, region, option);
    free(replaced);
    return res;
fail:
    free(replaced);
    return -1;
}

static void renderCaptures(renderer *r, line *line, pattern *p,
 OnigRegion *region);

static void renderLine(renderer *r, line *line, size_t begin, size_t end,
 size_t stackBase)
{
    if (begin == end)
        return;
    OnigRegion *endWhileRegion = onig_region_new();
    size_t offset = begin;
    size_t maxOffset = offset;
    for (size_t i = stackBase; i < r->stackDepth; ++i) {
        pattern *p = r->stack[i].s->whilePattern;
        if (!p)
            continue;
        int res = backreferencingSearch(r, r->stack[i], &r->stack[i].whileRegex,
         p, r->bytes + line->begin, r->bytes + line->endIncludingNewline,
         r->bytes + offset, r->bytes + end, endWhileRegion,
         ONIG_OPTION_NOT_BEGIN_POSITION);
        if (res < 0) {
            popStack(r, i);
            break;
        }
        renderCaptures(r, line, p, endWhileRegion);
        r->stack[i].outerBegin = line->begin + endWhileRegion->beg[0];
        r->stack[i].innerBegin = line->begin + endWhileRegion->end[0];
        offset = line->begin + endWhileRegion->end[0];
    }
    size_t matchesWithoutProgress = 0;
    while (matchesWithoutProgress < 32) {
        activeState a = r->stack[r->stackDepth - 1];
        if (!a.s)
            break;
        int options = 0;
        if (offset > a.innerBegin)
            options |= ONIG_OPTION_NOT_BEGIN_POSITION;
#ifdef LOG
        fprintf(stderr, "%zu %.*s\n", offset, (int)(end - offset), (char *)(r->bytes + offset));
#endif
        int endres = -1;
        if (a.s->endPattern) {
            endres = backreferencingSearch(r, a,
             &r->stack[r->stackDepth - 1].endRegex, a.s->endPattern,
             r->bytes + line->begin, r->bytes + line->endIncludingNewline,
             r->bytes + offset, r->bytes + end, endWhileRegion, options);
        }
        int matchpos;
        int res = onig_regset_search(a.s->regset,
         r->bytes + line->begin, r->bytes + line->endIncludingNewline,
         r->bytes + offset, r->bytes + end,
         ONIG_REGSET_POSITION_LEAD, options, &matchpos);
        if (res >= 0 && (endres < 0 || matchpos < endWhileRegion->beg[0] ||
         (a.s->applyEndPatternLast && matchpos == endWhileRegion->beg[0]))) {
#ifdef LOG
            fprintf(stderr, "match %d (%p) in %p\n", res, onig_regset_get_regex(a.s->regset, res), a.s);
            for (int i = 0; i < onig_regset_number_of_regex(a.s->regset); ++i)
                fprintf(stderr, "%d %p\n", i, onig_regset_get_regex(a.s->regset, i));
#endif
            OnigRegion *region = onig_regset_get_region(a.s->regset, res);
            patternInState p = a.s->patterns[res];
            renderCaptures(r, line, p.p, region);

            if (p.type == BEGIN) {
                // push the current state onto the stack.
                if (r->stackDepth == sizeof(r->stack)/sizeof(r->stack[0])) {
#ifdef LOG
                    fprintf(stderr, "renderLine(): stack overflow\n");
#endif
                    break;
                }
                OnigRegion *beginRegion = 0;
                if (p.to->endPattern && p.to->endPattern->backreferencingPattern) {
                    beginRegion = onig_region_new();
                    onig_region_copy(beginRegion, region);
                }
                r->stack[r->stackDepth++] = (activeState){
                    .s = p.to,
                    .p = p.p,
                    .beginRegion = beginRegion,
                    .beginOffset = line->begin,
                    .outerBegin = line->begin + region->beg[0],
                    .outerSeq = r->seq,
                    .innerBegin = line->begin + region->end[0],
                    .innerSeq = r->seq + 1,
                };
                r->seq += 2;
            }
            if (line->begin + region->end[0] > maxOffset) {
                matchesWithoutProgress = 0;
                maxOffset = line->begin + region->end[0];
            } else
                matchesWithoutProgress++;
            offset = line->begin + region->end[0];
        } else if (endres >= 0) {
#ifdef LOG
            fprintf(stderr, "match end (%p)\n", a.s->endPattern->re);
#endif
            renderCaptures(r, line, a.s->endPattern, endWhileRegion);
            // pop a state off the stack and add its scope ranges.
            if (r->stackDepth <= stackBase) {
#ifdef LOG
                fprintf(stderr, "renderLine(): stack underflow\n");
#endif
                break;
            }
            addScopeRange(line, a.p->innerScope, a.innerSeq, a.innerBegin,
             line->begin + endWhileRegion->beg[0]);
            addScopeRange(line, a.p->outerScope, a.outerSeq, a.outerBegin,
             line->begin + endWhileRegion->end[0]);
            popStack(r, r->stackDepth - 1);
            offset = line->begin + endWhileRegion->end[0];
            continue;
        } else {
            // no match found.
            break;
        }
    }
    // add ranges for all open scopes.
    for (size_t i = stackBase; i < r->stackDepth; ++i) {
        activeState a = r->stack[i];
        if (!a.p)
            continue;
        addScopeRange(line, a.p->outerScope, a.outerSeq, a.outerBegin, end);
        addScopeRange(line, a.p->innerScope, a.innerSeq, a.innerBegin, end);
    }
    onig_region_free(endWhileRegion, 1);
}

static void renderCaptures(renderer *r, line *line, pattern *p,
 OnigRegion *region)
{
    for (int i = 0; i < region->num_regs; ++i) {
#ifdef LOG
        fprintf(stderr, "%d: %d - %d\n", i, region->beg[i], region->end[i]);
#endif
        if (region->beg[i] < 0)
            continue;
        if (p->captureScopes[i] != 0) {
            addScopeRange(line, p->captureScopes[i], r->seq++,
             line->begin + region->beg[i], line->begin + region->end[i]);
        } else if (p->captureStates[i]) {
            if (r->stackDepth == sizeof(r->stack)/sizeof(r->stack[0])) {
                fprintf(stderr, "renderLine(): stack overflow\n");
                continue;
            }
            size_t depth = r->stackDepth++;
            r->stack[depth] = (activeState){
                .s = p->captureStates[i],
            };
            // recursively render using the match range.
            renderLine(r, line, line->begin + region->beg[i],
             line->begin + region->end[i], r->stackDepth);
            popStack(r, depth);
        }
    }
}

static int compareScopes(const void *aa, const void *bb)
{
    scope a = *(const scope *)aa;
    scope b = *(const scope *)bb;
    if (a.offset < b.offset)
        return -1;
    if (a.offset > b.offset)
        return 1;
    if (a.ty == SCOPE_END && b.ty == SCOPE_BEGIN)
        return -1;
    if (a.ty == SCOPE_BEGIN && b.ty == SCOPE_END)
        return 1;
    int dir = a.ty == SCOPE_BEGIN ? 1 : -1;
    if (a.startOffset < b.startOffset)
        return -dir;
    if (a.startOffset > b.startOffset)
        return dir;
    if (a.endOffset < b.endOffset)
        return dir;
    if (a.endOffset > b.endOffset)
        return -dir;
    if (a.seq < b.seq)
        return -dir;
    if (a.seq > b.seq)
        return dir;
    return 0;
}

bool renderNextLine(renderer *r, line *line)
{
    if (r->offset >= r->length)
        return false;
    line->scopesLength = 0;
    line->begin = r->offset;
    line->end = advanceToNextLine(r->bytes, r->length, &r->offset);
    line->endIncludingNewline = r->offset;
    renderLine(r, line, line->begin, line->endIncludingNewline, 1);
    qsort(line->scopes, line->scopesLength, sizeof(scope), compareScopes);
    return true;
}

void freeLine(line line)
{
    free(line.scopes);
}
