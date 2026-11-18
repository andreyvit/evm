package gox_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestCoverageOriginalGoXBranchesAndPooledSession(t *testing.T) {
	const source = `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, member bool) {
 <section>
 if member {
 <strong>"Welcome"</strong>
 } else {
 <span>"Join"</span>
 }
 </section>
}`
	template, err := gox.Compile("coverage.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := template.Link()
	if err != nil {
		t.Fatal(err)
	}
	coverage := evm.NewCoverage(p.EVM())
	for _, member := range []bool{false, true} {
		if _, err := p.Render(context.Background(), []values.Value{values.Bool(member)}, gox.RenderOptions{Coverage: coverage}); err != nil {
			t.Fatal(err)
		}
	}
	lines := coverage.Lines()
	conditionFound := false
	for _, line := range lines {
		if line.File == "coverage.gox" && line.Branches != 0 {
			if line.Line != 7 || line.Branches != 1 {
				t.Fatalf("branch must map to the condition, not its parameter: %+v", line)
			}
			conditionFound = true
		}
	}
	if !conditionFound {
		t.Fatal("missing condition coverage")
	}
	for _, line := range lines {
		if line.File == "coverage.gox" && !line.Covered() {
			t.Errorf("incomplete: %+v", line)
		}
	}
	for _, line := range []int{8, 10} {
		found := false
		for _, l := range lines {
			if l.File == "coverage.gox" && l.Line == line && l.Executed > 0 {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing original line %d: %+v", line, lines)
		}
	}
	before := coverage.Instructions()
	if _, err := p.Render(context.Background(), []values.Value{values.Bool(true)}, gox.RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	after := coverage.Instructions()
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("pooled render retained coverage")
		}
	}
}

func TestCoverageMultilineXMLAndClosure(t *testing.T) {
	const source = `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, show bool) {
 body := func() {
 if show {
 <em>"Yes"</em>
 } else {
 <em>"No"</em>
 }
 }
 <Card
 body={body}
 />
}
func Card(b *gox.Builder, body func()) {
 <article>
 body()
 </article>
}`
	template, err := gox.Compile("components.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := template.Link()
	if err != nil {
		t.Fatal(err)
	}
	c := evm.NewCoverage(p.EVM())
	for _, show := range []bool{true, false} {
		if _, err := p.Render(context.Background(), []values.Value{values.Bool(show)}, gox.RenderOptions{Coverage: c}); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range c.Lines() {
		if l.File == "components.gox" && !l.Covered() {
			t.Errorf("incomplete: %+v", l)
		}
	}
}

func TestCoverageCountsSourceBranchesNotGeneratedNilFallbacks(t *testing.T) {
	template, err := gox.Compile("fields.gox", `package plugin

import "github.com/andreyvit/evm/gox"

type Item struct { Name string }
func Page(b *gox.Builder, show bool) {
 item := &Item{Name:"Hello"}
 if show {
 <p>item.Name</p>
 } else {
 <p>"Hidden"</p>
 }
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := template.Link()
	if err != nil {
		t.Fatal(err)
	}
	c := evm.NewCoverage(p.EVM())
	for _, show := range []bool{true, false} {
		if _, err := p.Render(context.Background(), []values.Value{values.Bool(show)}, gox.RenderOptions{Coverage: c}); err != nil {
			t.Fatal(err)
		}
	}
	branches := 0
	for _, line := range c.Lines() {
		if line.File != "fields.gox" {
			continue
		}
		branches += line.Branches
		if !line.Covered() {
			t.Errorf("incomplete source coverage: %+v", line)
		}
	}
	if branches != 1 {
		t.Fatalf("source branches = %d, want 1", branches)
	}
	synthetic := false
	for _, instruction := range c.Instructions() {
		if instruction.Branch && instruction.Position.Line == 0 && instruction.Count > 0 {
			synthetic = true
		}
	}
	if !synthetic {
		t.Fatal("generated guard missing from raw instruction coverage")
	}
}

func TestCoverageFusedLoopHasOneSourceCondition(t *testing.T) {
	template, err := gox.Compile("loop.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 for i := 1; i <= 9; i++ {
 <span>i</span>
 }
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := template.Link()
	if err != nil {
		t.Fatal(err)
	}
	c := evm.NewCoverage(p.EVM())
	if _, err := p.Render(context.Background(), nil, gox.RenderOptions{Coverage: c}); err != nil {
		t.Fatal(err)
	}
	branches := 0
	for _, line := range c.Lines() {
		if line.File != "loop.gox" {
			continue
		}
		branches += line.Branches
		if !line.Covered() {
			t.Errorf("incomplete source coverage: %+v", line)
		}
	}
	if branches != 1 {
		t.Errorf("source conditions = %d, want 1", branches)
	}
	rawBranches := 0
	for _, instruction := range c.Instructions() {
		if instruction.Position.File == "loop.gox" && instruction.Branch {
			rawBranches++
		}
	}
	if rawBranches != 2 {
		t.Errorf("raw loop branches = %d, want 2", rawBranches)
	}
}

func TestCoverageNilLoweringDoesNotUseAnotherFilesPosition(t *testing.T) {
	options := gox.Options{ParseFrontmatter: func([]byte) (any, error) { return nil, nil }}
	tmpl, err := options.CompileSources(map[string]string{
		"first.gox": "---\ntitle: Metadata\n---\n\npackage plugin\n",
		"page.gox": `package plugin

import "github.com/andreyvit/evm/gox"

type State struct { Ready, Active bool }

func Page(b *gox.Builder, s *State) {
 <p>{s.Ready && s.Active}</p>
}
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range evm.NewCoverage(p.EVM()).Lines() {
		if line.File == "first.gox" {
			t.Fatalf("metadata file has executable coverage: %+v", line)
		}
	}
}
