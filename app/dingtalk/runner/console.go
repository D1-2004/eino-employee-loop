package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
)

// ConsoleUsage explains the console line syntax.
const ConsoleUsage = `console: each line is a message. "text" is a direct message from you; ` +
	`"#Name: text" is a group message from Name that does not @ the employee; ` +
	`"@Name: text" is a group message from Name that @s it ("Name:" is optional). Ctrl-D quits.`

// Console is the terminal channel for local debugging: every input line is a
// message to the employee, and its replies and decisions are printed. Lines
// are handled one at a time, so scripted runs are deterministic; Listen
// returns at the end of the input.
//
// Line syntax (see ConsoleUsage): plain text goes to the direct
// conversation "console-dm"; a "#" or "@" prefix goes to the group
// "console-group", where "@" mentions the employee and "Name:" picks the
// sender.
type Console struct {
	// In and Out default to stdin and stdout.
	In  io.Reader
	Out io.Writer
	// User is the sender of direct messages (default "user").
	User string
	// Employee is the employee's name in the output (default "employee").
	Employee string
	// Echo prints every input message too, for piped input.
	Echo bool

	mu     sync.Mutex
	run    string
	n      int
	done   chan error
	failed int
}

// Conversation ids of the console.
const (
	ConsoleDirect = "console-dm"
	ConsoleGroup  = "console-group"
)

// Listen reads lines until the input ends or ctx does, and waits for each
// message to be handled before reading the next. At the end of the input it
// returns an error if any wake failed.
func (c *Console) Listen(ctx context.Context, deliver func(Message)) error {
	c.run = "console-" + strconv.FormatInt(time.Now().Unix(), 36)
	c.done = make(chan error, 1)
	in := c.In
	if in == nil {
		in = os.Stdin
	}
	lines := make(chan string)
	var readErr error
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		readErr = sc.Err()
	}()
	total := 0
	for {
		var line string
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l, ok := <-lines:
			if !ok {
				if readErr != nil {
					return readErr
				}
				if c.failed > 0 {
					return fmt.Errorf("console: %d of %d wakes failed", c.failed, total)
				}
				return nil
			}
			line = l
		}
		m, ok := c.message(line)
		if !ok {
			continue
		}
		total++
		if c.Echo {
			text := m.Text
			if m.Mentioned {
				text = "@" + c.employee() + " " + text
			}
			c.printf(m, "%s: %s", m.SenderName, text)
		}
		deliver(m)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-c.done:
			if err != nil {
				c.failed++
			}
		}
	}
}

// message parses one input line.
func (c *Console) message(line string) (Message, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Message{}, false
	}
	user := c.User
	if user == "" {
		user = "user"
	}
	m := Message{ConversationID: ConsoleDirect, SenderID: user, SenderName: user, Text: line, Direct: true, At: time.Now()}
	if line[0] == '#' || line[0] == '@' {
		m.ConversationID, m.Direct, m.Mentioned = ConsoleGroup, false, line[0] == '@'
		m.Text = strings.TrimSpace(line[1:])
		// "Name:" (or "Name：") without spaces names the sender.
		if i := strings.IndexAny(m.Text, ":："); i > 0 && !strings.ContainsAny(m.Text[:i], " \t") {
			_, width := utf8.DecodeRuneInString(m.Text[i:])
			m.SenderID, m.SenderName = m.Text[:i], m.Text[:i]
			m.Text = strings.TrimSpace(m.Text[i+width:])
		}
	}
	if m.Text == "" {
		return Message{}, false
	}
	c.n++
	m.MessageID = c.run + "-" + strconv.Itoa(c.n)
	return m, true
}

// Send prints a reply.
func (c *Console) Send(_ context.Context, m Message, text string) (string, error) {
	c.printf(m, "%s: %s", c.employee(), text)
	return m.MessageID + "-reply", nil
}

// handled prints what a reply does not show (staying quiet, a message kept
// as history, a failure) and lets Listen read the next line.
func (c *Console) handled(m Message, out loop.Outcome, err error) {
	switch {
	case err != nil:
		c.printf(m, "(%s failed: %v)", c.employee(), err)
	case out.Kind == loop.Quiet:
		if reason := quietReason(out); reason != "" {
			c.printf(m, "(%s stays quiet: %s)", c.employee(), reason)
		} else {
			c.printf(m, "(%s stays quiet)", c.employee())
		}
	case out.Kind == "":
		c.printf(m, "(not addressed: %s keeps it as history)", c.employee())
	}
	if c.done != nil {
		c.done <- err
	}
}

func (c *Console) employee() string {
	if c.Employee != "" {
		return c.Employee
	}
	return "employee"
}

func (c *Console) printf(m Message, format string, args ...any) {
	where := "[dm]"
	if !m.Direct {
		where = "[group]"
	}
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(out, where+" "+format+"\n", args...)
}

// quietReason returns the reason argument of the wake's stay_quiet call.
func quietReason(out loop.Outcome) string {
	for i := len(out.Transcript) - 1; i >= 0; i-- {
		for _, call := range out.Transcript[i].ToolCalls {
			if call.Function.Name != "stay_quiet" {
				continue
			}
			var args struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			return strings.TrimSpace(args.Reason)
		}
	}
	return ""
}
