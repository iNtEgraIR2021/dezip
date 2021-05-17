// tmlanguage.h
// author: ian henderson <ian@ianhenderson.org>

#ifndef tmlanguage_h
#define tmlanguage_h

#include <stdbool.h>
#include <stdlib.h>

void initialize(void);

typedef enum scopeType scopeType;
typedef int scopeName;
typedef struct renderer renderer;
typedef struct line line;
typedef struct pattern pattern;
typedef struct scope scope;
typedef struct state state;

// *error must be freed by the caller if set.
pattern *createPattern(const unsigned char *regex, size_t len,
 char **error);
// backreferencing end/while patterns can reference captures from the begin
// pattern.
pattern *createBackreferencingPattern(const unsigned char *regex,
 size_t len, char **error);
// these inner and outer scopes only apply for patterns added using addBegin()
// or addEnd().
void setInnerScope(pattern *p, scopeName scope);
void setOuterScope(pattern *p, scopeName scope);
// if captureName can be parsed as an integer, it will be interpreted as a
// numbered capture.  otherwise, it will be interpreted as a named capture.  to
// apply a scope to an entire match, use the capture named "0".
void setCaptureScope(pattern *p, const unsigned char *captureName, size_t len, scopeName scope);
// instead of just applying a scope, enter a state and match patterns to apply further scopes.
void setCaptureState(pattern *p, const unsigned char *captureName, size_t len, state *s);
void freePattern(pattern *p);

state *createState(void);
void addMatch(state *s, pattern *match);
void addBegin(state *from, state *to, pattern *begin);
void setEnd(state *s, pattern *end, bool applyLast);
void setWhile(state *s, pattern *while_);
void freeState(state *s);

renderer *createRenderer(const unsigned char *bytes, size_t len, state *startState);
void freeRenderer(renderer *r);

bool firstLineMatch(const unsigned char *bytes, size_t len, pattern* p);

enum scopeType {
    SCOPE_BEGIN,
    SCOPE_END,
};
struct scope {
    scopeType ty;
    // the name passed to the setScope function.
    scopeName name;
    // the offset of this scope marker within the renderer byte array.
    size_t offset;
    // the start/end offsets of the scope (used for sorting).  they may not be
    // within the line range.
    size_t startOffset;
    size_t endOffset;
    // a sequence number to break ties during sorting.
    size_t seq;
};
struct line {
    scope *scopes;
    size_t scopesLength;
    size_t scopesCapacity;

    // the range of the line within the renderer byte array, not including the
    // final newline.
    size_t begin;
    size_t end;
    // the end of the line including the final newline.
    size_t endIncludingNewline;
};
bool renderNextLine(renderer *r, line *outLine);
void freeLine(line line);

#endif
