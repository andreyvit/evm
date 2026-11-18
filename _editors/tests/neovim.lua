local ok, err = pcall(function()
  assert(vim.bo.filetype == "gox", "GoX filetype detection failed")
  local parser = vim.treesitter.get_parser(0, "gox")
  assert(not parser:parse(true)[1]:root():has_error(), "GoX fixture failed to parse")
  local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false)

  local function check(context, token, language, capture)
    for row, line in ipairs(lines) do
      local column = line:find(context, 1, true)
      if column then
        column = column + assert(context:find(token, 1, true)) - 2
        for _, match in ipairs(vim.treesitter.get_captures_at_pos(0, row - 1, column)) do
          if match.lang == language and (not capture or match.capture == capture) then
            return
          end
        end
        error(("Missing %s capture at %s"):format(language, context))
      end
    end
    error("Missing fixture text: " .. context)
  end

  check("---", "---", "gox", "punctuation.special")
  check('name: "Test card"', "name", "yaml")
  check("func Page", "func", "gox", "keyword")
  check("left<right", "<", "gox", "operator")
  check("<Label", "Label", "gox", "tag")
  check("args:{key()", "key", "gox", "function.call")
  check("const marker", "const", "javascript")
  check("{=name}", "{=", "gox", "punctuation.special")
  check(".badge", "badge", "css")
  check("{=accent}", "{=", "gox", "punctuation.special")
end)

if not ok then
  io.stderr:write(err .. "\n")
  vim.cmd("cquit")
end
vim.cmd("quitall!")
