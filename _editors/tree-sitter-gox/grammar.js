// tree-sitter-go v0.25.0 (1547678a9da59885853f5f5cc8a99cc203fa2e2c).
const go = require('./upstream/go');

const terminator = choice(/\n/, ';', '\0');
const tagName = /[a-zA-Z0-9_:.-]+/;

module.exports = grammar(go, {
  name: 'gox',

  externals: $ => [
    $._gox_open_start,
    $.gox_script_text,
    $.gox_style_text,
    $.gox_frontmatter_open,
    $.gox_frontmatter_body,
    $.gox_frontmatter_close,
  ],

  rules: {
    source_file: $ => seq(
      optional($.gox_frontmatter),
      repeat(choice(
        $._gox_markup,
        seq(choice($._statement, $._top_level_declaration), choice(terminator, $._gox_markup)),
      )),
      optional($._top_level_declaration),
    ),

    gox_frontmatter: $ => seq(
      $.gox_frontmatter_open,
      optional($.gox_frontmatter_body),
      $.gox_frontmatter_close,
    ),

    // A tag ends its statement, including when it is an assignment's RHS.
    // Like the preprocessor, leave tags flat so Go controls block scope.
    statement_list: $ => choice(
      seq(repeat($._gox_terminated_statement), $._statement),
      repeat1($._gox_terminated_statement),
      seq(repeat($._gox_terminated_statement),
        alias($.empty_labeled_statement, $.labeled_statement)),
    ),

    _gox_terminated_statement: $ => choice(
      $._gox_markup,
      prec(1, $._gox_assignment),
      seq($._statement, choice(terminator, $._gox_markup)),
    ),

    _simple_statement: ($, goStatement) => choice(goStatement, $._gox_assignment),

    _gox_assignment: $ => choice(
      alias($._gox_short_declaration, $.short_var_declaration),
      alias($._gox_reassignment, $.assignment_statement),
    ),

    _gox_short_declaration: $ => seq(
      field('left', $.expression_list),
      ':=',
      field('right', $._gox_self_closing_tag),
    ),

    _gox_reassignment: $ => seq(
      field('left', $.expression_list),
      field('operator', '='),
      field('right', $._gox_self_closing_tag),
    ),

    _gox_markup: $ => choice(
      $.gox_open_tag,
      $._gox_self_closing_tag,
      $.gox_close_tag,
      $.gox_fragment_open,
      $.gox_fragment_close,
      $.gox_comment,
      $.gox_doctype,
      $.gox_script,
      $.gox_style,
    ),

    _gox_lt: $ => alias($._gox_open_start, '<'),

    gox_open_tag: $ => seq($._gox_tag_head, '>'),

    _gox_self_closing_tag: $ => alias($.gox_self_closing_tag, $.gox_open_tag),

    gox_self_closing_tag: $ => seq($._gox_tag_head, '/>'),

    _gox_tag_head: $ => seq(
      $._gox_lt,
      field('name', alias(token.immediate(tagName), $.gox_tag_name)),
      repeat($.gox_attribute),
    ),

    gox_close_tag: $ => seq(
      '</',
      field('name', alias(token.immediate(tagName), $.gox_tag_name)),
      '>',
    ),

    gox_fragment_open: $ => seq($._gox_lt, token.immediate('>')),
    gox_fragment_close: _ => '</>',

    gox_attribute: $ => seq(
      field('name', $.gox_attribute_name),
      optional(field('key', alias($.gox_attribute_key, $.gox_interpolation))),
      optional(seq('=', field('value', choice(
        $.gox_attribute_string,
        $.gox_interpolation,
      )))),
    ),

    gox_attribute_name: _ => tagName,

    gox_attribute_key: $ => seq(token.immediate('{'), $._expression, '}'),

    gox_attribute_string: $ => choice(
      seq('"', repeat(choice(token.immediate(/[^"{]+/), $.gox_interpolation)), token.immediate('"')),
      seq("'", repeat(choice(token.immediate(/[^'{]+/), $.gox_interpolation)), token.immediate("'")),
    ),

    gox_interpolation: $ => seq('{', $._expression, '}'),

    gox_comment: _ => token(seq('<!--', /([^-]|-[^-]|--+[^>-])*--+>/)),
    gox_doctype: _ => token(seq(/<![dD][oO][cC][tT][yY][pP][eE]/, /[^>]*>/)),

    gox_script: $ => seq(
      $._gox_lt,
      field('name', alias(token.immediate('script'), $.gox_tag_name)),
      repeat($.gox_attribute),
      choice('/>', seq('>', optional($.gox_script_body), '</',
        alias(token.immediate(/[sS][cC][rR][iI][pP][tT]/), $.gox_tag_name), '>')),
    ),

    gox_style: $ => seq(
      $._gox_lt,
      field('name', alias(token.immediate('style'), $.gox_tag_name)),
      repeat($.gox_attribute),
      choice('/>', seq('>', optional($.gox_style_body), '</',
        alias(token.immediate(/[sS][tT][yY][lL][eE]/), $.gox_tag_name), '>')),
    ),

    gox_script_body: $ => repeat1(choice($.gox_script_text, $.gox_raw_interpolation)),
    gox_style_body: $ => repeat1(choice($.gox_style_text, $.gox_raw_interpolation)),
    gox_raw_interpolation: $ => seq('{=', $._expression, '}'),
  },
});
