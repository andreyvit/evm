set runtimepath^=./_editors/vim
filetype on
syntax on
edit _editors/tests/highlight.gox

function! s:Check(context, token, expected)
  for lnum in range(1, line('$'))
    let start = stridx(getline(lnum), a:context)
    if start >= 0
      let col = start + stridx(a:context, a:token) + 1
      let actual = synIDattr(synID(lnum, col, 1), 'name')
      call assert_equal(a:expected, actual, a:context)
      return
    endif
  endfor
  call assert_report('Missing fixture text: ' . a:context)
endfunction

call assert_equal('gox', &filetype)
call s:Check('---', '---', 'goxFence')
call s:Check('name: "Test card"', 'name', 'yamlBlockMappingKey')
call s:Check('func Page', 'func', 'goDeclaration')
call s:Check('left<right', '<', '')
call s:Check('<Label', 'Label', 'goxTagName')
call s:Check('args:{key()', 'args:', 'goxAttribute')
call s:Check('Hello {name}', '{', 'goxFence')
call s:Check('keep <literal>', 'literal', 'goString')
call s:Check('// <comment>', 'comment', 'goComment')
call s:Check('const greeting', 'const', 'javaScriptReserved')
call s:Check('{=name}', '{=', 'goxFence')
call s:Check('.badge', 'badge', 'cssClassName')
call s:Check('{=accent}', '{=', 'goxFence')
call s:Check('<Widget', 'Widget', 'goxTagName')

if !empty(v:errors)
  call writefile(v:errors, '/dev/stderr')
  cquit
endif
quitall!
