; Go captures adapted from tree-sitter-go (MIT, see ../../THIRD-PARTY-LICENSES).
; Identifiers

(type_identifier) @type
(field_identifier) @property
(identifier) @variable

(package_identifier) @namespace
(label_name) @label

; Function calls

(call_expression
  function: (identifier) @function.call)

(call_expression
  function: (identifier) @function.builtin
  (#match? @function.builtin "^(append|cap|clear|close|complex|copy|delete|imag|len|make|max|min|new|panic|print|println|real|recover)$"))

(call_expression
  function: (selector_expression
    field: (field_identifier) @function.method.call))

; Function definitions

(function_declaration
  name: (identifier) @function)

(method_declaration
  name: (field_identifier) @function.method)

; Operators

[
  "--"
  "-"
  "-="
  ":="
  "!"
  "!="
  "..."
  "*"
  "*"
  "*="
  "/"
  "/="
  "&"
  "&&"
  "&="
  "%"
  "%="
  "^"
  "^="
  "+"
  "++"
  "+="
  "<-"
  "<"
  "<<"
  "<<="
  "<="
  "="
  "=="
  ">"
  ">="
  ">>"
  ">>="
  "|"
  "|="
  "||"
  "~"
] @operator

; Keywords

[
  "break"
  "case"
  "chan"
  "const"
  "continue"
  "default"
  "defer"
  "else"
  "fallthrough"
  "for"
  "func"
  "go"
  "goto"
  "if"
  "import"
  "interface"
  "map"
  "package"
  "range"
  "return"
  "select"
  "struct"
  "switch"
  "type"
  "var"
] @keyword

; Literals

[
  (interpreted_string_literal)
  (raw_string_literal)
  (rune_literal)
] @string

(escape_sequence) @string.escape

[
  (int_literal)
  (float_literal)
  (imaginary_literal)
] @number

[
  (true)
  (false)
  (nil)
  (iota)
] @constant.builtin

(comment) @comment

; GoX markup
(gox_tag_name) @tag
(gox_attribute_name) @attribute
(gox_attribute_string) @string
(gox_comment) @comment
(gox_doctype) @preproc

(gox_open_tag ["<" ">" "/>"] @punctuation.bracket)
(gox_close_tag ["</" ">"] @punctuation.bracket)
(gox_fragment_open ["<" ">"] @punctuation.bracket)
(gox_fragment_close) @punctuation.bracket
(gox_script ["<" ">" "</" "/>"] @punctuation.bracket)
(gox_style ["<" ">" "</" "/>"] @punctuation.bracket)
(gox_interpolation ["{" "}"] @punctuation.special)
(gox_raw_interpolation ["{=" "}"] @punctuation.special)

[(gox_frontmatter_open) (gox_frontmatter_close)] @punctuation.special
