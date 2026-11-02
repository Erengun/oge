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
		{"cat <<'EOF' >> notes.md\nsee /etc and ~ for it\nEOF", false},
		{"(cd web && cat <<'EOF' > x.js\n// see /etc for defaults\nEOF\n)", false},
		{"cat >>\"notes.md\" <<'EOF'\nsee /etc and ~ for it\nEOF\ngit add notes.md", false},
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
		// A redirect to a device is no named file, and a here-document
		// piped on or read by a substitution is not data.
		{"cat <<'EOF' > /dev/stdout | sh\ncat /etc/passwd\nEOF", true},
		{"cat <<'EOF' >/dev/stdout | bash\ncat ~/.ssh/id_rsa\nEOF", true},
		{"cat <<'EOF' 1>/dev/stdout | sh\ncat /etc/passwd\nEOF", true},
		{"cat <<'EOF' 2>&1 >/dev/stderr |& sh\ncat /etc/passwd\nEOF", true},
		{"cat <<'EOF' > /tmp/../dev/stdout | sh\ncat /etc/passwd\nEOF", true},
		{"cat <<'EOF' > \"/dev/stdout\" | sh\ncat /etc/passwd\nEOF", true},
		{"cat <<'EOF' > x.txt | sh\ncat /etc/passwd\nEOF", true},
		{"(cat <<'EOF' > x.txt) | sh\ncat /etc/passwd\nEOF", true},
		{"bash <(cat <<'EOF' >/dev/stdout\ncat /etc/passwd\nEOF\n)", true},
		{"python3 <(cat <<'EOF' >/dev/stdout\nprint(open('/etc/passwd').read())\nEOF\n)", true},
		{"cat $(cat <<'EOF' > /dev/stdout\n/etc/passwd\nEOF\n)", true},
		{"bash <(cat <<'EOF' > x.txt\ncat /etc/passwd\nEOF\n)", true},
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

		// tee to /dev/null writes the body to the named file; /dev/null
		// feeds nothing on.
		{"tee x.txt > /dev/null <<'EOF'\nsee /etc\nEOF", false},
		// Traded on purpose (#341): a here-document written to a file and
		// then run is not stopped here. A Write tool call followed by
		// `sh x.sh` was never stopped either; the sandbox is the backstop
		// (ADR-0013 as amended: the check is a speed bump).
		{"cat <<'EOF' > x.sh && sh x.sh\ncat /etc/passwd\nEOF", false},

		// A lone / in a sed, jq or awk script, a commit message, or the
		// operator of inline code is not the root (#348). source reads
		// a relative path inside the Workspace.
		{"sed -i 's/foo()/bar()/' a.go", false},
		{"sed -i 's/^/  /' a.txt", false},
		{"sed -i 's|// old|// new|' a.go", false},
		{"sed -i '' 's/^/  /' a.txt", false},
		{"sed -ne 's/a/b/p' a.txt | sort", false},
		{"sed -e 's/^/  /' -e 's/x/y/' a.txt", false},
		{"jq -r '.title // \"-\"' a.json", false},
		{"jq '.a / .b' a.json", false},
		{"awk -F/ '{print $2}' f", false},
		{"awk '{print $1 / 2}' f", false},
		{"git commit -m \"Split read / write paths\"", false},
		{"git commit -m \"read / write\"", false},
		{"git -C sub commit -am \"a // b\"", false},
		{"python3 -c 'print(10 / 2)'", false},
		{"python3 -c 'print(7 // 2)'", false},
		{"node -e 'console.log(10 / 2)'", false},
		{"source .venv/bin/activate && pytest", false},
		{". ./env.sh", false},
		{"cd sub && source ./env.sh", false},
		{"sed -n 's/a/b/p' a.txt | wc -l", false},

		// Escapes the narrowing must not open.
		{"echo 'x /' | xargs grep -r", true},
		{"sed 's/^/  /' a.txt | xargs ls", true},
		{"awk 'BEGIN{print \"/\"}' | xargs ls", true},
		{"sed 's/^/  /' a.txt | sh", true},
		{"sed 's/^/  /' a.txt | env bash", true},
		{"(sed 's/^/  /' a.txt; echo) | xargs ls", true},
		{"grep -r $(awk 'BEGIN{print \"/\"}')", true},
		{"awk 'BEGIN{print \"/\"}'", true},
		{"jq -n '\"/\"'", true},
		{"sed -n p /etc/hosts", true},
		{"sed -n p /", true},
		{"sed -i 's/a/b/' /etc/hosts", true},
		{"sed -f /etc/passwd a.txt", true},
		{"jq . /etc/passwd", true},
		{"jq --arg a x . /etc/passwd", true},
		{"awk 1 /etc/passwd", true},
		{"awk -f /etc/passwd", true},
		{"git commit -F /etc/passwd", true},
		{"git commit -m \"a / b\" -F /etc/passwd", true},
		{"git commit -m \"see /etc/passwd\"", true},
		{"python3 -c 'import os; os.listdir(\"/\")'", true},
		{"python3 -c 'import os; os.listdir(\"/\") / 2'", true},
		{"python3 -c \"print(open('/etc/passwd').read())\"", true},
		{"python3 -c 'import os; os.system(\"ls /\")'", true},
		{"bash -c 'ls /'", true},
		{"sh -c \"sed 's/a/b/' /etc/hosts\"", true},
		{"source ../outside/x.sh", true},
		{"source /etc/profile", true},
		{"source ~/.bashrc", true},
		{"source $HOME/x", true},
		{"source", true},
		{". ../x.sh", true},
		{"source $(echo x)", true},
		{"source ./*.sh", true},

		// Review of #348: the script word is not always the script, a sed
		// can run a command, a q(/) is a literal, a process substitution
		// is a consumer, and gawk has flags that take a file.
		{"jq /etc/passwd -f", true},
		{"jq -n /etc/passwd --from-file", true},
		{"jq -nf /etc/passwd", true},
		{"jq -rf /etc/passwd .", true},
		{"sed 's|.*|cd /; cat etc/passwd|e' a.txt", true},
		{"sed '1e cd /; cat etc/passwd' a.txt", true},
		{"perl -e 'chdir q(/); print qx(cat etc/passwd)'", true},
		{"ruby -e 'Dir.chdir(%q(/)); puts File.read(%q(etc/passwd))'", true},
		{"perl -e 'opendir D, q(/); print readdir D'", true},
		{"sed 's|^|cd / ; cat etc/passwd #|' a.txt > >(sh)", true},
		{"sed 's|^|cd / ; cat etc/passwd #|' a.txt | tee >(sh)", true},
		{"gawk -E /etc/passwd", true},
		{"gawk -i /etc/passwd 'BEGIN{}'", true},
		{"jq -r '.a / .b' a.json", false},
		{"sed -n 's/one/two/p;$!d' a.txt", false},
		{"cd sub && source ../../x.sh", true},
		{"source \"$X\"", true},
		{"eval ls", true},
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
