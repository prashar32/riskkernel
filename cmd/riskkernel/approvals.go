package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/prashar32/riskkernel/internal/config"
)

// runApprovals implements `riskkernel approvals <list|approve|deny>`.
//
// Unlike `runs`/`audit` (which read the local store), these talk to the running
// daemon's HTTP API — resolving an approval must wake the goroutine blocked inside
// the daemon, which a direct store write cannot do.
func runApprovals(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: riskkernel approvals <list | approve <id> | deny <id>>")
	}
	switch args[0] {
	case "list":
		return approvalsList()
	case "approve", "deny":
		if len(args) < 2 {
			return fmt.Errorf("usage: riskkernel approvals %s <id> [--reason <text>]", args[0])
		}
		reason := flagValue(args[2:], "--reason")
		return approvalsResolve(args[1], args[0] == "approve", reason)
	default:
		return fmt.Errorf("unknown approvals subcommand %q (want list|approve|deny)", args[0])
	}
}

type approvalItem struct {
	ID         string         `json:"id"`
	RunID      string         `json:"runId"`
	StepIndex  int            `json:"stepIndex"`
	Tool       string         `json:"tool"`
	SideEffect string         `json:"sideEffect"`
	Arguments  map[string]any `json:"arguments"`
	Status     string         `json:"status"`
	CreatedAt  string         `json:"createdAt"`
}

func approvalsList() error {
	c, err := newDaemonClient()
	if err != nil {
		return err
	}
	var items []approvalItem
	if err := c.getJSON("/v1/approvals?status=pending", &items); err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no pending approvals")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tRUN\tSTEP\tTOOL\tSIDE EFFECT\tCREATED")
	for _, a := range items {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", a.ID, a.RunID, a.StepIndex, a.Tool, dash(a.SideEffect), a.CreatedAt)
	}
	return tw.Flush()
}

func approvalsResolve(id string, approve bool, reason string) error {
	c, err := newDaemonClient()
	if err != nil {
		return err
	}
	// Discover the approval's run id from the pending list (the resolve endpoint
	// is keyed by run id per the api/v1 contract).
	var items []approvalItem
	if err := c.getJSON("/v1/approvals?status=pending", &items); err != nil {
		return err
	}
	runID := ""
	for _, a := range items {
		if a.ID == id {
			runID = a.RunID
			break
		}
	}
	if runID == "" {
		return fmt.Errorf("no pending approval with id %s", id)
	}

	decision := "deny"
	if approve {
		decision = "approve"
	}
	body, _ := json.Marshal(map[string]string{
		"approvalId": id, "decision": decision, "reason": reason, "decidedBy": "cli",
	})
	if err := c.post("/v1/runs/"+runID+"/approve", body); err != nil {
		return err
	}
	fmt.Printf("%sd approval %s\n", decision, id)
	return nil
}

// --- minimal daemon HTTP client ---

type daemonClient struct {
	base   string
	token  string
	client *http.Client
}

func newDaemonClient() (*daemonClient, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return &daemonClient{
		base:   fmt.Sprintf("http://localhost:%d", cfg.Port),
		token:  cfg.APIToken,
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *daemonClient) do(method, path string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach daemon at %s (is `riskkernel serve` running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

func (c *daemonClient) getJSON(path string, out any) error {
	raw, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *daemonClient) post(path string, body []byte) error {
	_, err := c.do(http.MethodPost, path, body)
	return err
}

// flagValue returns the value following name in args, or "".
func flagValue(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
