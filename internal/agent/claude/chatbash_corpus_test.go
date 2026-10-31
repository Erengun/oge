//go:build unix

package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// chatCorpus is a corpus of the commands a coding agent runs in a Chat,
// each with whether the hard deny must stop it (#341). The allowed rows
// are shapes agents run (some taken from recorded Claude Code sessions in
// this repo); the denied rows are escapes, several found by review. It is
// read from a Workspace holding sub/, web/ and pkg/; tool is a toolchain
// directory a Chat command may read.
func chatCorpus(tool string) []struct {
	cmd  string
	deny bool
} {
	return []struct {
		cmd  string
		deny bool
	}{
		// Legitimate: compound commands, pipes, subshells, env prefixes.
		{"cd sub && go test ./...", false},
		{"git -C . status", false},
		{"go test ./... 2>&1 | tail -30", false},
		{"cd sub && go test ./... 2>&1 | tail -30", false},
		{"FOO=1 go build ./...", false},
		{"env GOFLAGS=-mod=mod go vet ./...", false},
		{"(cd web && npm test)", false},
		{"make test", false},
		{"ls -la", false},
		{"grep -rn foo .", false},
		{"find . -name '*.go' | xargs wc -l", false},
		{"rg foo", false},
		{"jq . file.json", false},
		{"sed -n 1,40p a.go", false},
		{"go test -run 'TestX|TestY' ./pkg/...", false},
		{"tail -f /dev/null", false},
		{"which go", false},
		{"/usr/bin/env bash -c 'go test ./...'", false},
		{"go build ./... && go vet ./... 2>/dev/null", false},
		{"go test ./... > test.log 2>&1; tail -5 test.log", false},
		{"cd web && npm run build && cd ..", false},
		{"cd web; npm ci; cd ..", false},
		{"mkdir -p sub/build && cd sub/build && cmake ..", false},
		{"find . -name '*.go' -not -path './vendor/*' -exec grep -l TODO {} \\;", false},
		{"for f in pkg/*.go; do gofmt -l \"$f\"; done", false},
		{"if [ -f go.mod ]; then go test ./...; fi", false},
		{"wc -l $(git ls-files '*.go') | sort -n | tail", false},
		{"git log --oneline -10 && git status --short", false},
		{"git diff HEAD~1 -- pkg/", false},
		{"ls " + filepath.Join(tool, "src"), false},
		{"timeout 60 go test -count=1 ./pkg/...", false},
		{"python3 -c 'import sys; print(sys.version)'", false},
		{"node -e \"console.log(require('./package.json').version)\"", false},
		// A here-document is data when cat or tee writes it to a file.
		{"cat <<'EOF' > x.go\npackage x\n\n// Hello isn't exported from /etc.\nfunc hello() {}\nEOF", false},
		{"cat > notes.md <<'EOF'\nSee ~/notes and ../other for why it's done.\nEOF\ngo test ./...", false},
		{"git commit -m \"$(cat <<'EOF'\nFix the parser\n\nCo-Authored-By: Claude <noreply@anthropic.com>\nEOF\n)\"", false},
		// A comment is not a command.
		{"go test ./... # the old run in /tmp didn't help", false},
		// Patterns and scripts that only look like absolute paths.
		{"grep -rn '/api/v1' .", false},
		{"rg -n \"/healthz\" --type go", false},
		{"sed -n '/^func main/,/^}/p' main.go", false},
		{"awk '/^func/ {print $2}' a.go", false},
		{"awk -F'/' '{print $NF}' paths.txt", false},
		{"tr '/' '.' < paths.txt", false},
		{"cut -d '/' -f 2 paths.txt", false},
		{"env LC_ALL=C tr '/' '.' < paths.txt", false},
		{"cat <<-EOF > x.txt\n\tsee /etc and ~ for it\n\tEOF", false},
		{"go test ./... &> test.log && echo ok >| done.txt", false},
		{"bash -c 'grep -rn \"/api\" .'", false},
		// URLs are not paths.
		{"curl -s http://localhost:8080/healthz", false},
		{"git clone https://github.com/x/y.git vendor/y", false},
		// A recorded edit script: a // line inside a Python string.
		{"python3 - <<'EOF'\np='a.go'; s=open(p).read()\ns=s.replace('x', '''x\n// note\n''')\nopen(p,'w').write(s)\nEOF", false},
		{"cat <<'EOF' > x.txt 2>&1\ncat /etc/passwd\nEOF", false},
		// A program runs where the cd before it left the shell.
		{"cd sub && python3 - <<'EOF'\nprint(open('../go.mod').read())\nEOF", false},
		{"cd sub && bash -c 'cat ../go.mod'", false},

		// Escapes: each must be denied.
		{"cat ~/.ssh/id_rsa", true},
		{"cd .. && ls", true},
		{"cat ../../etc/passwd", true},
		{"git -C /tmp status", true},
		{"ls $HOME", true},
		{"python3 -c \"open('/etc/x')\"", true},
		{"cd / && head etc/hosts", true},
		{"cd && cat .ssh/id_rsa", true},
		{"cd sub; cd ../..; ls", true},
		{"go test ./... 2>&1 > /tmp/out.txt", true},
		{"find / -name '*.pem'", true},
		{"ls /", true},
		{"eval \"$CMD\"", true},
		{"env bash -c \"$CMD\"", true},
		{"FOO=1 cd .. && ls", true},
		{"/usr/bin/env sh -c 'cd; cat .ssh/id_rsa'", true},
		{"xargs -I{} sh -c \"$X\"", true},
		{"find . -exec sh -c \"$X\" \\;", true},
		{"cd nosuch; cd ..; ls", true},
		{"cd nosuch && make; cd ..", true},
		{"(cd web && npm test); cd ..", true},
		{"cd web | true; cd ..", true},
		{"cd -", true},
		{"bash <<'EOF'\ncat /etc/passwd\nEOF", true},
		{"python3 - <<'EOF'\nprint(open('/etc/passwd').read())\nEOF", true},
		{"cat <<'EOF' | sh\ncat /etc/passwd\nEOF", true},
		{"xargs cat <<'EOF'\n/etc/passwd\nEOF", true},
		{"cat <<EOF > x.txt\n$(cat /etc/passwd)\nEOF", true},
		{"go test ./... 2>&1 | tee ~/log.txt", true},
		{"curl -s file:///etc/passwd", true},
		{"rg --ignore-file /etc/passwd foo", true},
		{"sed -n '1r /etc/passwd' a.go", true},
		{"sed 'w /tmp/out' a.go", true},
		{"awk '{print > \"/tmp/out\"}' a.go", true},
		{"awk 'BEGIN{system(\"cat /\" \"etc/passwd\")}'", true},
		{"awk 'BEGIN{while((getline l < \"/etc/passwd\")>0) print l}'", true},
		{"grep -f /etc/passwd a.go", true},
		{"cut -d '/' -f 2 /etc/passwd", true},
		{"jq . /etc/hosts.json", true},
		{"bash -x -c \"$CMD\"", true},
		{"/usr/bin/env bash -x -c \"$CMD\"", true},
		{"cat '/etc/passwd'", true},
		{"ls \"/\"", true},
		{"find '/' -name '*.pem'", true},
		{"tr '/' '.' < /etc/passwd", true},
		{"cat \"$HOME/.netrc\"", true},
		{"ls -la ~", true},
		{"cd sub && (cd .. && cd .. && ls)", true},
		// A lone / after a flag is the root unless the command takes a
		// delimiter there.
		{"find -d / -name '*.pem'", true},
		{"find -d / -name passwd -exec cat {} +", true},
		{"ls -F /", true},
		{"ls -t /", true},
		{"ls -d /", true},
		{"tree -d /", true},
		{"rsync -a -t / ./copy", true},
		{"column -t /", true},
		// x://.. is a path through a directory named x:.
		{"cat x://../../../../../../../../../etc/passwd", true},
		{"git -C x://../../../../../../../.. status", true},
		// A here-document delimiter the shell reads differently, or one
		// that never closes, would hide the commands after it.
		{"cat <<$'EOF' > x\nhi\nEOF\ncat /etc/passwd\n$EOF", true},
		{"cat <<$\"EOF\" > x\nhi\nEOF\ncat /etc/passwd\n$EOF", true},
		{"cat <<\"E\\\"OF\" > x\nhi\nE\"OF\ncat /etc/passwd\nE\\\"OF", true},
		// A here-document that reaches a shell through a substitution.
		{"cat <<'EOF' > >(sh)\ncat /etc/passwd\nEOF", true},
		{"tee <<'EOF' >(bash)\ncat /etc/passwd\nEOF", true},
		{"bash <(cat <<'EOF'\ncat /etc/passwd\nEOF\n)", true},
		{"$(cat <<'EOF'\ncat /etc/passwd\nEOF\n)", true},
		{"{\ncat <<'EOF'\ncat /etc/passwd\nEOF\n} | sh", true},
		// A quoted part is a pattern only when nothing can join it into
		// a path.
		{"python3 -c \"import os;print(open(os.path.normpath('/nope/../etc/passwd')).read())\"", true},
		{"python3 -c \"open('/e' + 'tc/passwd')\"", true},
		{"awk 'BEGIN{system(\"cat /e\" \"tc/passwd\")}'", true},
		{"awk -v 'a=/e' 'BEGIN{system(\"cat \" a \"tc/passwd\")}'", true},
		{"python3 - <<'EOF'\nx = '/e'\nprint(open(x + 'tc/passwd').read())\nEOF", true},
		{"cat \"/e\"*/passwd", true},
		{"cat '/e'*'/passwd'", true},
		{"cat '/'*", true},
		{"ls '/'*/", true},
		{"cat \"/e$(echo tc)/passwd\"", true},
		{"cd sub && python3 - <<'EOF'\nprint(open('../../x').read())\nEOF", true},

		// Known false stops, a product decision pending: a lone / in
		// quoted text is read as the root, since text like 'x /' can
		// reach a command as arguments (echo 'x /' | xargs grep -r), and
		// source runs a script the policy can't read.
		{"sed -i 's/foo()/bar()/' a.go", true},
		{"sed -i 's/^/  /' a.txt", true},
		{"sed -i 's|// old|// new|' a.go", true},
		{"jq -r '.title // \"-\"' a.json", true},
		{"git commit -m \"Split read / write paths\"", true},
		{"python3 -c 'print(10 / 2)'", true},
		{"source .venv/bin/activate && pytest", true},
	}
}

func TestChatBashCorpus(t *testing.T) {
	ws := t.TempDir()
	for _, d := range []string{"sub", "web", "pkg"} {
		if err := os.Mkdir(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := newPolicy(agent.LaunchSpec{Role: "implementer", Workspace: ws, Permissions: agent.AutoPermissions,
		DenyRead: []string{filepath.Join(t.TempDir(), "private")}}, "h")
	tool := t.TempDir()
	p.readOK = withReal(tool)
	for _, c := range chatCorpus(tool) {
		if got := p.escapes(c.cmd); got != c.deny {
			kind := "false stop"
			if c.deny {
				kind = "miss"
			}
			t.Errorf("%s: %q: escapes = %v", kind, c.cmd, got)
		}
	}
}
