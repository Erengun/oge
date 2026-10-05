package cli

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
)

var runtimeGOOS = runtime.GOOS

var modeNotes = map[pipeline.Mode]string{
	pipeline.Fast:     "implementer, then Öge's Check. No independent verifier and no held-out tests",
	pipeline.Standard: "implementer, then a fresh verifier writes held-out tests, then Öge's Check",
	pipeline.Blind:    "a verifier writes held-out tests from the Task alone, then the implementer, then Öge's Check",
}

// renderDryRun prints what a Run would do. It is the startup summary
// without a Run id or Preflight.
func renderDryRun(w io.Writer, t task.Task, f *pipeline.Frozen, hasProjectConfig bool) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("Dry run: nothing was started.")
	p("")
	p("Task       %s", t.Title)
	if len(t.Criteria) == 0 {
		p("Criteria   none")
	}
	for i, c := range t.Criteria {
		label := "          "
		if i == 0 {
			label = "Criteria  "
		}
		p("%s %-5s %s", label, c.ID, c.Text)
	}
	p("")
	p("Mode       %s: %s", f.Mode, modeNotes[f.Mode])
	if hasProjectConfig {
		p("Config     %s from the Snapshot, schema %d", pipeline.ConfigPath, pipeline.SchemaVersion)
	} else {
		p("Config     none in the Snapshot; CLI flags only")
	}
	p("Pipeline   default (frozen, compiled graph %s)", f.Hash[:12])
	p("           %s", f.Graph.Shape(f.Verify))
	p("           mandatory Gates: %s", strings.Join(f.Graph.MandatoryGates(), " · "))
	for _, s := range f.Stages {
		bind := s.Agent
		if s.Model != "" {
			bind += ":" + s.Model
		}
		extra := ""
		if s.Role == "verifier" {
			extra = "   fresh Session every Attempt"
		}
		p("  %-10s %-14s network %s%s   (%s)", s.Name, bind, s.Network, extra, s.Source)
	}
	for _, c := range f.Checks {
		report := c.Report
		if report == "" {
			report = "no report"
		}
		p("  %-10s %s   %s · timeout %s · network off   (%s)", "Check", c.Run, report, duration(c.Timeout), c.Source)
	}
	p("  %-10s %s", "tests", strings.Join(f.Project.TestGlobs, "  "))
	if len(f.Project.OutputGlobs) > 0 {
		p("  %-10s %s", "output", strings.Join(f.Project.OutputGlobs, "  "))
	} else {
		p("  %-10s none: every new file is Ambiguous until you decide", "output")
	}
	if f.Setup.Run != "" {
		network := f.Setup.Network
		if network == "" {
			network = "off"
		}
		p("  %-10s %s   network %s", "setup", f.Setup.Run, network)
	}
	l := f.Limits
	limits := fmt.Sprintf("retries %d/Stage · send-backs %d · Attempts %d/Run", l.Retries, l.SendBacks, l.Attempts)
	if f.Mode != pipeline.Fast {
		limits += fmt.Sprintf(" · user requests %d · Oracle growth %d Attempts, %d/Attempt, %d/Run",
			l.UserRequests, l.OracleGrowthAttempts, l.OracleGrowthPerAttempt, l.OracleGrowthPerRun)
	}
	limits += fmt.Sprintf(" · Stage %s active, idle %s", duration(l.StageTimeout), duration(l.StageIdleTimeout))
	p("  %-10s %s", "limits", limits)

	if len(t.Criteria) == 0 {
		p("")
		p("! No acceptance criteria found in the Task.")
		if f.Mode == pipeline.Fast {
			p("!   Send-back feedback can name no criteria. Add a \"## Acceptance criteria\" list to fix this.")
		} else {
			p("!   Held-out tests will be reported as unmapped, and send-back feedback can name no criteria.")
			p("!   Add a \"## Acceptance criteria\" list to fix this.")
		}
	}
	if len(f.TrustWeakening) > 0 {
		p("")
		p("! Trust-weakening options in effect")
		for _, w := range f.TrustWeakening {
			line := fmt.Sprintf("!   %-24s from %s", w.Option, sourceName(w.Source))
			if w.Detail != "" {
				line += "   (" + w.Detail + ")"
			}
			p("%s", line)
		}
	}
	p("")
	p("Valid. A Run would start from here.")
}

func sourceName(s pipeline.Source) string {
	if s == pipeline.FromProject {
		return pipeline.ConfigPath
	}
	return string(s)
}

// duration prints 10m0s as 10m and 1h0m0s as 60m.
func duration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}
