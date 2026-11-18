if exists('b:current_syntax')
  finish
endif

syntax include @goxGo syntax/go.vim
" GoX's own groups disable tags in parentheses and re-enable them in braces.
silent! syntax clear goParen goBlock
unlet! b:current_syntax
syntax include @goxYaml syntax/yaml.vim
unlet! b:current_syntax
syntax include @goxJavaScript syntax/javascript.vim
unlet! b:current_syntax
syntax include @goxCss syntax/css.vim
unlet! b:current_syntax
syntax case match

syntax cluster goxCode contains=@goxGo,goxParen,goxBracket,goxBlock
syntax cluster goxMarkup contains=goxTag,goxFragment,goxHtmlComment,goxDoctype,goxScriptHeader,goxStyleHeader

syntax region goxSource start=/\%^/ end=/\%$/ transparent contains=@goxCode,@goxMarkup,goxFrontmatter
syntax region goxFrontmatter matchgroup=goxFence start=/\%1l^---$/ end=/^---$/ keepend contained contains=@goxYaml
syntax region goxParen start=/(/ end=/)/ transparent contained contains=@goxCode
syntax region goxBracket start=/\[/ end=/\]/ transparent contained contains=@goxCode
syntax region goxBlock start=/{/ end=/}/ transparent contained contains=@goxCode,@goxMarkup

syntax region goxFragment matchgroup=goxTagDelimiter start=+<\ze/\?>+ end=+>+ contained
syntax region goxHtmlComment start=/<!--/ end=/-->/ contained
syntax match goxDoctype /\c<!doctype[^>]*>/ contained

syntax cluster goxTagContents contains=goxAttribute,goxString,goxInterpolation
syntax region goxTag matchgroup=goxTagDelimiter start=+<\ze/\?[a-zA-Z0-9_:.-]+ end=+/\?>+
      \ contained contains=@goxTagContents,goxTagName
syntax match goxAttribute /[a-zA-Z0-9_:.-]\+/ contained
syntax match goxTagName /\%(<\/\?\)\@<=[a-zA-Z0-9_:.-]\+/ contained
syntax region goxString start=/"/ end=/"/ contained contains=goxInterpolation
syntax region goxString start=/'/ end=/'/ contained contains=goxInterpolation
syntax region goxInterpolation matchgroup=goxFence start=/{/ end=/}/ contained contains=@goxExpr
syntax cluster goxExpr contains=@goxGo,goxExprBrace,goxExprParen,goxExprBracket
syntax region goxExprBrace start=/{/ end=/}/ transparent contained contains=@goxExpr
syntax region goxExprParen start=/(/ end=/)/ transparent contained contains=@goxExpr
syntax region goxExprBracket start=/\[/ end=/\]/ transparent contained contains=@goxExpr

" Parse the attributes before selecting the raw body or self-closing delimiter.
syntax region goxScriptHeader matchgroup=goxTagDelimiter start=+<\zescript\%(\_s\|[/>]\)\@=+ end=+\ze/\?>+
      \ contained contains=@goxTagContents,goxTagName nextgroup=goxScript,goxTagDelimiter
syntax region goxStyleHeader matchgroup=goxTagDelimiter start=+<\zestyle\%(\_s\|[/>]\)\@=+ end=+\ze/\?>+
      \ contained contains=@goxTagContents,goxTagName nextgroup=goxStyle,goxTagDelimiter
syntax match goxTagDelimiter +/>+ contained
syntax region goxScript matchgroup=goxTagDelimiter start=+>+ end=+\c</script\s*>+ keepend
      \ contained contains=@goxJavaScript,goxRawInterpolation
syntax region goxStyle matchgroup=goxTagDelimiter start=+>+ end=+\c</style\s*>+ keepend
      \ contained contains=@goxCss,goxRawInterpolation
syntax region goxRawInterpolation matchgroup=goxFence start=/{=/ end=/}/
      \ contained containedin=cssDefinition,cssAttrRegion,cssStringQ,cssStringQQ,javaScriptStringD,javaScriptStringS,javaScriptStringT contains=@goxExpr

highlight default link goxFence Special
highlight default link goxTagDelimiter Delimiter
highlight default link goxTagName Statement
highlight default link goxAttribute Identifier
highlight default link goxString String
highlight default link goxHtmlComment Comment
highlight default link goxDoctype PreProc

syntax sync fromstart
let b:current_syntax = 'gox'
