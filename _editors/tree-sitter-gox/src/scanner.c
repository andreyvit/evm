#include "tree_sitter/parser.h"

enum TokenType {
  OPEN_START,
  SCRIPT_TEXT,
  STYLE_TEXT,
  FRONTMATTER_OPEN,
  FRONTMATTER_BODY,
  FRONTMATTER_CLOSE,
};

void *tree_sitter_gox_external_scanner_create(void) { return NULL; }
void tree_sitter_gox_external_scanner_destroy(void *payload) {}
unsigned tree_sitter_gox_external_scanner_serialize(void *payload, char *buffer) { return 0; }
void tree_sitter_gox_external_scanner_deserialize(void *payload, const char *buffer, unsigned length) {}

static bool tag_name_byte(int32_t c) {
  return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') ||
         ('0' <= c && c <= '9') || c == '-' || c == '_' || c == ':' || c == '.';
}

static bool whitespace(int32_t c) {
  return c == ' ' || ('\t' <= c && c <= '\r');
}

static int32_t ascii_lower(int32_t c) {
  return 'A' <= c && c <= 'Z' ? c + ('a' - 'A') : c;
}

static bool scan_raw_text(TSLexer *lexer, const char *tag) {
  bool has_text = false;
  while (!lexer->eof(lexer)) {
    // Leave the delimiter for the grammar. The next candidate can start
    // immediately after a partial match, such as in "<</script>".
    lexer->mark_end(lexer);
    if (lexer->lookahead == '{') {
      lexer->advance(lexer, false);
      if (lexer->lookahead == '=') {
        return has_text;
      }
    } else if (lexer->lookahead == '<') {
      lexer->advance(lexer, false);
      if (lexer->lookahead == '/') {
        lexer->advance(lexer, false);
        const char *letter = tag;
        while (*letter && ascii_lower(lexer->lookahead) == *letter) {
          lexer->advance(lexer, false);
          ++letter;
        }
        if (!*letter && (lexer->lookahead == '>' || whitespace(lexer->lookahead))) {
          return has_text;
        }
      }
    } else {
      lexer->advance(lexer, false);
    }
    has_text = true;
  }
  lexer->mark_end(lexer);
  return has_text;
}

static bool scan_frontmatter_delimiter(TSLexer *lexer, bool allow_eof) {
  for (int i = 0; i < 3; ++i) {
    if (lexer->lookahead != '-') return false;
    lexer->advance(lexer, false);
  }
  if (lexer->lookahead == '\r') lexer->advance(lexer, false);
  if (lexer->lookahead == '\n') {
    lexer->advance(lexer, false);
    return true;
  }
  return allow_eof && lexer->eof(lexer);
}

static bool scan_frontmatter_open(TSLexer *lexer) {
  if (lexer->get_column(lexer) != 0) return false;
  if (lexer->lookahead == 0xfeff) lexer->advance(lexer, false);
  if (!scan_frontmatter_delimiter(lexer, false)) return false;
  lexer->mark_end(lexer);
  lexer->result_symbol = FRONTMATTER_OPEN;
  return true;
}

static bool scan_frontmatter_body(TSLexer *lexer, bool allow_body) {
  // Keep the closing delimiter out of the range passed to the injected parser.
  bool has_body = false;
  while (!lexer->eof(lexer)) {
    lexer->mark_end(lexer);
    if (scan_frontmatter_delimiter(lexer, true)) {
      if (has_body) {
        lexer->result_symbol = FRONTMATTER_BODY;
      } else {
        lexer->mark_end(lexer);
        lexer->result_symbol = FRONTMATTER_CLOSE;
      }
      return true;
    }
    if (!allow_body) return false;
    while (!lexer->eof(lexer) && lexer->lookahead != '\n') lexer->advance(lexer, false);
    if (!lexer->eof(lexer)) lexer->advance(lexer, false);
    has_body = true;
  }
  if (!has_body) return false;
  lexer->mark_end(lexer);
  lexer->result_symbol = FRONTMATTER_BODY;
  return true;
}

bool tree_sitter_gox_external_scanner_scan(void *payload, TSLexer *lexer,
                                           const bool *valid_symbols) {
  // Tree-sitter enables all external tokens during error recovery.
  if (valid_symbols[SCRIPT_TEXT] && valid_symbols[STYLE_TEXT]) {
    return false;
  }
  if (valid_symbols[FRONTMATTER_OPEN] && (lexer->lookahead == '-' || lexer->lookahead == 0xfeff)) {
    return scan_frontmatter_open(lexer);
  }
  if (valid_symbols[FRONTMATTER_CLOSE]) {
    return scan_frontmatter_body(lexer, valid_symbols[FRONTMATTER_BODY]);
  }
  if (valid_symbols[SCRIPT_TEXT] || valid_symbols[STYLE_TEXT]) {
    bool script = valid_symbols[SCRIPT_TEXT];
    lexer->result_symbol = script ? SCRIPT_TEXT : STYLE_TEXT;
    return scan_raw_text(lexer, script ? "script" : "style");
  }

  if (!valid_symbols[OPEN_START]) {
    return false;
  }

  while (whitespace(lexer->lookahead)) {
    lexer->advance(lexer, true);
  }
  if (lexer->lookahead != '<') {
    return false;
  }
  lexer->advance(lexer, false);
  lexer->mark_end(lexer);
  if (lexer->lookahead == '>' || tag_name_byte(lexer->lookahead)) {
    lexer->result_symbol = OPEN_START;
    return true;
  }
  return false;
}
