// Package itbench runs the Drill Down Harness on the public ITBench-AA SRE
// scenarios and scores it with the open-source grader, one task at a time.
//
// Each task: download the scenario snapshot, start a Docker sandbox with the
// data mounted read-only and no network, let the agent investigate with a
// shell tool, collect /home/user/agent_output.json, grade it, delete the data.
package itbench

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arsaljamal/drill-down-harness/agent"
	"github.com/arsaljamal/drill-down-harness/retry"
)

//go:embed prompt.md grader/grade.py grader/matching.py
var files embed.FS

const (
	dataset = "ArtificialAnalysis/ITBench-AA"
	image   = "drill-itbench-sandbox:1"
	home    = "/home/user"
)

// Sandbox image: the Python tools named in the official prompt, plus pyyaml
// for the grader. Built once; tasks then run with --network none.
const dockerfile = `FROM python:3.13-slim
RUN pip install --no-cache-dir pyyaml drain3==0.9.11 numpy pandas
RUN useradd -m user
WORKDIR /home/user
`

// Options controls a benchmark run.
type Options struct {
	Model     agent.Model
	ModelName string
	Retry     retry.Policy
	Scenarios []string // empty: all public scenarios
	Limit     int      // stop after this many tasks (0: no limit)
	OutDir    string
	WorkDir   string  // where snapshots are downloaded
	Keep      bool    // keep snapshot data after each task
	StopBelow float64 // stop early if the running mean drops below this...
	MinTasks  int     // ...after at least this many tasks
	MaxTurns  int
	MaxTokens int64
	Timeout   time.Duration
	PriceIn   float64 // USD per million input tokens, for cost estimates
	PriceOut  float64 // USD per million output tokens
	Oracle    bool    // submit the answer key instead of calling a model (setup check)
	Log       func(format string, args ...any)
}

// TaskResult is one line of results.jsonl.
type TaskResult struct {
	Scenario  string  `json:"scenario"`
	Reward    float64 `json:"reward"`
	Stop      string  `json:"stop"`
	Turns     int     `json:"turns"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	Seconds   float64 `json:"seconds"`
	Answered  bool    `json:"answered"`
	Infra     bool    `json:"-"` // failed before or after the agent ran (download, sandbox, grader): not a score
	Error     string  `json:"error,omitempty"`
}

// Run executes the benchmark and returns the per-task results.
func Run(ctx context.Context, o Options) ([]TaskResult, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if err := ensureImage(ctx, o.Log); err != nil {
		return nil, err
	}
	ids := o.Scenarios
	if len(ids) == 0 {
		var err error
		if ids, err = listScenarios(ctx); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, err
	}
	resultsFile, err := os.OpenFile(filepath.Join(o.OutDir, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer resultsFile.Close()

	// Resume: tasks already in results.jsonl count towards the mean and are skipped.
	done := map[string]bool{}
	var results []TaskResult
	sum := 0.0
	if b, err := os.ReadFile(filepath.Join(o.OutDir, "results.jsonl")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r TaskResult
			if json.Unmarshal([]byte(line), &r) == nil && r.Scenario != "" {
				done[r.Scenario] = true
				results = append(results, r)
				sum += r.Reward
			}
		}
		if len(results) > 0 {
			o.Log("resuming: %d task(s) already scored in %s", len(results), o.OutDir)
		}
	}
	ran, infraInARow := 0, 0
	for _, id := range ids {
		if done[id] {
			continue
		}
		if o.Limit > 0 && ran >= o.Limit {
			break
		}
		ran++
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		o.Log("\n=== %s (%d of %d) ===", id, len(results)+1, len(ids))
		r := runTask(ctx, o, id)
		if r.Infra {
			o.Log("%s: skipped, not scored (%s)", id, oneLine(r.Error, 300))
			if infraInARow++; infraInARow >= 2 {
				return results, fmt.Errorf("two tasks in a row failed for reasons outside the agent; stopping to avoid wasting quota")
			}
			continue
		}
		infraInARow = 0
		if quotaHit(r) {
			o.Log("%s: the model provider's quota is used up (%s). Not recording this task; run the same command later to resume.", id, oneLine(r.Error, 160))
			return results, fmt.Errorf("provider quota reached")
		}
		results = append(results, r)
		sum += r.Reward
		line, _ := json.Marshal(r)
		resultsFile.Write(append(line, '\n'))
		mean := sum / float64(len(results))
		o.Log("%s: reward=%.2f stop=%s turns=%d cost=$%.2f | running mean %.3f over %d task(s)", id, r.Reward, r.Stop, r.Turns, r.CostUSD, mean, len(results))
		if o.StopBelow > 0 && len(results) >= o.MinTasks && mean < o.StopBelow {
			o.Log("stopping early: running mean %.3f is below the %.3f threshold", mean, o.StopBelow)
			break
		}
	}
	return results, nil
}

func runTask(ctx context.Context, o Options, id string) (res TaskResult) {
	res.Scenario = id
	start := time.Now()
	defer func() { res.Seconds = time.Since(start).Seconds() }()

	taskOut := filepath.Join(o.OutDir, id)
	os.MkdirAll(taskOut, 0o755)
	data := filepath.Join(o.WorkDir, id)
	if !o.Keep {
		defer os.RemoveAll(data)
	}

	o.Log("downloading snapshot…")
	if err := download(ctx, id, data, o.Log); err != nil {
		res.Error, res.Infra = "download: "+err.Error(), true
		return res
	}

	box, err := startSandbox(ctx, id, data)
	if err != nil {
		res.Error, res.Infra = "sandbox: "+err.Error(), true
		return res
	}
	defer box.stop()

	prompt, _ := files.ReadFile("prompt.md")
	tctx := ctx
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	model := o.Model
	if o.Oracle {
		orc, err := newOracle(data)
		if err != nil {
			res.Error = "oracle: " + err.Error()
			return res
		}
		model = orc
	}
	runner := &agent.Runner{
		Model:      model,
		Tools:      box,
		System:     "You are an SRE investigation agent working only through the run_shell tool. Keep each command's output small: use head, wc, grep, or short Python/pandas scripts instead of printing whole files.",
		Limits:     agent.Limits{MaxTurns: o.MaxTurns, MaxTokens: o.MaxTokens, MaxRepeats: 4},
		MaxOutput:  8192,
		ModelRetry: o.Retry,
		Compact:    agent.TrimOldToolResults(6, 2000),
		Observe: func(e agent.Event) {
			if e.Kind == "tool_call" || e.Kind == "retry" || e.Kind == "stop" {
				o.Log("  [turn %d] %s %s", e.Turn, e.Kind, oneLine(e.Message, 140))
			}
		},
	}
	run := runner.Run(tctx, string(prompt))
	res.Stop, res.Turns = string(run.Stop), run.Turns
	res.TokensIn, res.TokensOut = run.Usage.InputTokens, run.Usage.OutputTokens
	res.CostUSD = float64(res.TokensIn)/1e6*o.PriceIn + float64(res.TokensOut)/1e6*o.PriceOut
	if run.Err != nil {
		res.Error = run.Err.Error()
	}
	// A failed model call is the provider's or the harness's problem, not
	// the agent's reasoning: don't score it (quota is reported separately).
	if run.Stop == agent.StopModelError && !quotaHit(res) {
		res.Infra = true
	}
	if t, err := json.MarshalIndent(run.Messages, "", " "); err == nil {
		os.WriteFile(filepath.Join(taskOut, "transcript.json"), t, 0o644)
	}

	answer, err := box.readAnswer(ctx)
	if err == nil {
		res.Answered = true
		os.WriteFile(filepath.Join(taskOut, "answer.json"), answer, 0o644)
	}
	reward, err := grade(ctx, data, taskOut)
	if err != nil {
		res.Error, res.Infra = strings.TrimSpace(res.Error+"; grade: "+err.Error()), true
	}
	res.Reward = reward
	return res
}

// quotaHit reports a run that ended because the provider refused more
// requests (rate or daily limit), not because the agent failed.
func quotaHit(r TaskResult) bool {
	return r.Stop == string(agent.StopModelError) && (strings.Contains(r.Error, " 429") || strings.Contains(strings.ToLower(r.Error), "quota") || strings.Contains(strings.ToLower(r.Error), "rate limit"))
}

// ---------- dataset ----------

type treeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func listTree(ctx context.Context, path string) ([]treeEntry, error) {
	var all []treeEntry
	url := fmt.Sprintf("https://huggingface.co/api/datasets/%s/tree/main/%s?recursive=true", dataset, path)
	for url != "" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		var page []treeEntry
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", path, err)
		}
		all = append(all, page...)
		url = nextLink(resp.Header.Get("Link"))
	}
	return all, nil
}

func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		if strings.Contains(part, `rel="next"`) {
			s, e := strings.Index(part, "<"), strings.Index(part, ">")
			if s >= 0 && e > s {
				return part[s+1 : e]
			}
		}
	}
	return ""
}

func listScenarios(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://huggingface.co/api/datasets/%s/tree/main/sre", dataset), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []treeEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.Type == "directory" {
			ids = append(ids, filepath.Base(e.Path))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return scenarioNum(ids[i]) < scenarioNum(ids[j]) })
	return ids, nil
}

func scenarioNum(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "Scenario-"))
	return n
}

func download(ctx context.Context, id, dest string, log func(string, ...any)) error {
	entries, err := listTree(ctx, "sre/"+id)
	if err != nil {
		return err
	}
	var todo []treeEntry
	var total int64
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		target := filepath.Join(dest, strings.TrimPrefix(e.Path, "sre/"+id+"/"))
		if st, err := os.Stat(target); err == nil && st.Size() == e.Size {
			continue // already downloaded
		}
		todo = append(todo, e)
		total += e.Size
	}
	log("downloading %d file(s), %.0f MB", len(todo), float64(total)/1e6)

	jobs := make(chan treeEntry)
	errs := make(chan error, len(todo))
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				rel := strings.TrimPrefix(e.Path, "sre/"+id+"/")
				target := filepath.Join(dest, rel)
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					errs <- err
					continue
				}
				url := fmt.Sprintf("https://huggingface.co/datasets/%s/resolve/main/%s", dataset, e.Path)
				var last error
				for attempt := 1; attempt <= 5; attempt++ {
					if last = fetch(ctx, url, target, e.Size); last == nil || ctx.Err() != nil {
						break
					}
					log("  retrying %s (attempt %d): %v", rel, attempt+1, last)
					time.Sleep(time.Duration(attempt*attempt) * 2 * time.Second)
				}
				if last != nil {
					errs <- fmt.Errorf("%s: %w", rel, last)
				}
			}
		}()
	}
	for _, e := range todo {
		jobs <- e
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}
	log("download complete")
	return nil
}

// fetch downloads one file. A transfer that makes no progress for two
// minutes is abandoned so a dead connection can't hang the run.
func fetch(ctx context.Context, url, target string, size int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := target + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	progress := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-progress:
				t.Reset(2 * time.Minute)
			case <-t.C:
				cancel() // stalled
				return
			}
		}
	}()
	buf := make([]byte, 256<<10)
	var written int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			written += int64(n)
			select {
			case progress <- struct{}{}:
			default:
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if size > 0 && written != size {
		return fmt.Errorf("got %d of %d bytes", written, size)
	}
	return os.Rename(tmp, target)
}

// ---------- sandbox ----------

func ensureImage(ctx context.Context, log func(string, ...any)) error {
	if exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil {
		return nil
	}
	log("building sandbox image %s (one time)…", image)
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", image, "-")
	cmd.Stdin = strings.NewReader(dockerfile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build: %v\n%s", err, tail(string(out), 2000))
	}
	return nil
}

// sandbox is a running container with the snapshot mounted read-only. It is
// also the agent's toolbox: run_shell and finish.
type sandbox struct {
	name string
}

func startSandbox(ctx context.Context, id, data string) (*sandbox, error) {
	entries, err := os.ReadDir(data)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("drill-itbench-%s-%d", strings.ToLower(id), time.Now().UnixNano())
	args := []string{"run", "-d", "--rm", "--name", name, "--network", "none", "--memory", "6g", "--cpus", "2", "--pids-limit", "512", "--user", "user"}
	for _, e := range entries {
		if e.Name() == "ground_truth.yaml" || strings.HasSuffix(e.Name(), ".part") {
			continue // the answer key never enters the sandbox
		}
		abs, _ := filepath.Abs(filepath.Join(data, e.Name()))
		args = append(args, "-v", abs+":"+home+"/"+e.Name()+":ro")
	}
	args = append(args, image, "sleep", "infinity")
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, tail(string(out), 1000))
	}
	box := &sandbox{name: name}
	// Fail fast if Docker cannot see the data (e.g. a folder it does not share).
	check := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", "find "+home+" -type f | head -1")
	if out, _ := check.Output(); len(strings.TrimSpace(string(out))) == 0 {
		box.stop()
		return nil, fmt.Errorf("the sandbox sees no data; keep the download folder inside your home folder so Docker can share it")
	}
	return box, nil
}

func (s *sandbox) stop() {
	exec.Command("docker", "rm", "-f", s.name).Run()
}

func (s *sandbox) Tools(context.Context) ([]agent.ToolSpec, error) {
	return []agent.ToolSpec{
		{Name: "run_shell", Description: "Run a shell command in the sandbox (working directory /home/user, no network, data is read-only). Output is cut to 20,000 characters; 120 second limit.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}}},
		{Name: "finish", Description: "Submit the final diagnosis file when it is written.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Path to the JSON diagnosis, normally /home/user/agent_output.json"}}, "required": []any{"path"}}},
	}, nil
}

func (s *sandbox) Call(ctx context.Context, name string, input json.RawMessage) (agent.ToolResult, error) {
	switch name {
	case "run_shell":
		var in struct{ Command string }
		if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.Command) == "" {
			return agent.ToolResult{Text: "run_shell needs a non-empty command", IsError: true}, nil
		}
		cctx, cancel := context.WithTimeout(ctx, 130*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, "docker", "exec", "-w", home, s.name, "timeout", "-k", "5", "120", "sh", "-c", in.Command).CombinedOutput()
		text := tail(string(out), 20000)
		if err != nil {
			return agent.ToolResult{Text: fmt.Sprintf("%s\n[exit: %v]", text, err), IsError: true}, nil
		}
		if text == "" {
			text = "(no output)"
		}
		return agent.ToolResult{Text: text}, nil
	case "finish":
		if _, err := s.readAnswer(ctx); err != nil {
			return agent.ToolResult{Text: "No readable JSON at /home/user/agent_output.json: " + err.Error(), IsError: true}, nil
		}
		return agent.ToolResult{Text: "Submitted. The task is complete; reply with a one-line summary and stop."}, nil
	}
	return agent.ToolResult{Text: "unknown tool " + name, IsError: true}, nil
}

func (s *sandbox) readAnswer(ctx context.Context) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", s.name, "cat", home+"/agent_output.json").Output()
	if err != nil {
		return nil, err
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("not valid JSON")
	}
	return out, nil
}

// ---------- grading ----------

// grade runs the official grader in a throwaway container: grader scripts and
// the answer key in /tests, the answer in /workspace, reward in /logs.
func grade(ctx context.Context, data, taskOut string) (float64, error) {
	// Kept under the results folder: Docker runtimes such as Colima and Docker
	// Desktop only share the home folder, not the system temp folder.
	tests := filepath.Join(taskOut, "grader")
	if err := os.MkdirAll(tests, 0o755); err != nil {
		return 0, err
	}
	defer os.RemoveAll(tests)
	for _, f := range []string{"grade.py", "matching.py"} {
		b, _ := files.ReadFile("grader/" + f)
		os.WriteFile(filepath.Join(tests, f), b, 0o644)
	}
	gt, err := os.ReadFile(filepath.Join(data, "ground_truth.yaml"))
	if err != nil {
		return 0, fmt.Errorf("answer key missing: %w", err)
	}
	os.WriteFile(filepath.Join(tests, "ground_truth.yaml"), gt, 0o644)

	work := filepath.Join(taskOut, "workspace")
	logs := filepath.Join(taskOut, "logs")
	os.MkdirAll(work, 0o755)
	os.MkdirAll(logs, 0o777)
	if b, err := os.ReadFile(filepath.Join(taskOut, "answer.json")); err == nil {
		os.WriteFile(filepath.Join(work, "answer.json"), b, 0o644)
	}
	absTests, _ := filepath.Abs(tests)
	absWork, _ := filepath.Abs(work)
	absLogs, _ := filepath.Abs(logs)
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none",
		"-v", absTests+":/tests:ro", "-v", absWork+":/workspace:ro", "-v", absLogs+":/logs",
		image, "python3", "/tests/grade.py").CombinedOutput()
	os.WriteFile(filepath.Join(taskOut, "grader.log"), out, 0o644)
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, tail(string(out), 500))
	}
	b, err := os.ReadFile(filepath.Join(logs, "verifier", "reward.json"))
	if err != nil {
		return 0, err
	}
	var r struct{ Reward float64 }
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	return r.Reward, nil
}

// ---------- helpers ----------

func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("[first %d characters cut]\n", len(s)-max) + s[len(s)-max:]
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
