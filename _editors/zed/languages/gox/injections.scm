((gox_script_body (gox_script_text) @injection.content) @injection.host
 (#set! injection.language "javascript"))

((gox_style_body (gox_style_text) @injection.content) @injection.host
 (#set! injection.language "css"))

((gox_frontmatter_body) @injection.content
 (#set! injection.language "yaml"))
