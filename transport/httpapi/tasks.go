package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// TaskList is the answer to GET /v1/tasks.
type TaskList struct {
	Tasks []contract.TaskView `json:"tasks"`
}

// NewTaskManagerHandler serves the Task Manager boundary. Control bodies
// are contract.TaskControl; the path ref fills an empty TaskRef and must
// match a non-empty one.
func NewTaskManagerHandler(tm contract.TaskManager, opts ...Option) http.Handler {
	s := newServer(opts)
	s.mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var req contract.DispatchRequest
		if !s.decode(w, r, &req) {
			return
		}
		rc, err := tm.Dispatch(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rc)
	})
	s.mux.HandleFunc("GET /v1/tasks/{ref}", func(w http.ResponseWriter, r *http.Request) {
		v, err := tm.Get(r.Context(), r.URL.Query().Get("employee"), r.PathValue("ref"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	s.mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tq := contract.TaskQuery{Employee: q.Get("employee"), Scene: q.Get("scene"), Requester: q.Get("requester"), Text: q.Get("text")}
		if l := q.Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n < 0 {
				writeError(w, fmt.Errorf("%w: limit", contract.ErrInvalidRequest))
				return
			}
			tq.Limit = n
		}
		views, err := tm.Find(r.Context(), tq)
		if err != nil {
			writeError(w, err)
			return
		}
		if views == nil {
			views = []contract.TaskView{}
		}
		writeJSON(w, http.StatusOK, TaskList{Tasks: views})
	})
	controls := map[string]func(context.Context, contract.TaskControl) (contract.TaskReceipt, error){
		"steer": tm.Steer, "stop": tm.Stop, "continue": tm.Continue,
	}
	for op, f := range controls {
		s.mux.HandleFunc("POST /v1/tasks/{ref}/"+op, func(w http.ResponseWriter, r *http.Request) {
			var c contract.TaskControl
			if !s.decode(w, r, &c) {
				return
			}
			ref := r.PathValue("ref")
			if c.TaskRef == "" {
				c.TaskRef = ref
			} else if c.TaskRef != ref {
				writeError(w, fmt.Errorf("%w: task_ref does not match the path", contract.ErrInvalidRequest))
				return
			}
			rc, err := f(r.Context(), c)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, rc)
		})
	}
	return s
}

// TaskManagerClient talks to a remote Task Manager.
type TaskManagerClient struct{ c client }

var _ contract.TaskManager = (*TaskManagerClient)(nil)

// NewTaskManagerClient returns a client for a server rooted at baseURL.
func NewTaskManagerClient(baseURL string, opts ...Option) *TaskManagerClient {
	return &TaskManagerClient{c: newClient(baseURL, opts)}
}

// Dispatch implements contract.TaskManager.
func (c *TaskManagerClient) Dispatch(ctx context.Context, req contract.DispatchRequest) (contract.TaskReceipt, error) {
	var rc contract.TaskReceipt
	err := c.c.do(ctx, http.MethodPost, "/v1/tasks", req, &rc)
	return rc, err
}

// Get implements contract.TaskManager.
func (c *TaskManagerClient) Get(ctx context.Context, employee, ref string) (contract.TaskView, error) {
	var v contract.TaskView
	path := "/v1/tasks/" + url.PathEscape(ref) + "?" + url.Values{"employee": {employee}}.Encode()
	err := c.c.do(ctx, http.MethodGet, path, nil, &v)
	return v, err
}

// Find implements contract.TaskManager.
func (c *TaskManagerClient) Find(ctx context.Context, q contract.TaskQuery) ([]contract.TaskView, error) {
	vals := url.Values{"employee": {q.Employee}, "scene": {q.Scene}}
	if q.Requester != "" {
		vals.Set("requester", q.Requester)
	}
	if q.Text != "" {
		vals.Set("text", q.Text)
	}
	if q.Limit > 0 {
		vals.Set("limit", strconv.Itoa(q.Limit))
	}
	var list TaskList
	if err := c.c.do(ctx, http.MethodGet, "/v1/tasks?"+vals.Encode(), nil, &list); err != nil {
		return nil, err
	}
	return list.Tasks, nil
}

// Steer implements contract.TaskManager.
func (c *TaskManagerClient) Steer(ctx context.Context, tc contract.TaskControl) (contract.TaskReceipt, error) {
	return c.control(ctx, "steer", tc)
}

// Stop implements contract.TaskManager.
func (c *TaskManagerClient) Stop(ctx context.Context, tc contract.TaskControl) (contract.TaskReceipt, error) {
	return c.control(ctx, "stop", tc)
}

// Continue implements contract.TaskManager.
func (c *TaskManagerClient) Continue(ctx context.Context, tc contract.TaskControl) (contract.TaskReceipt, error) {
	return c.control(ctx, "continue", tc)
}

func (c *TaskManagerClient) control(ctx context.Context, op string, tc contract.TaskControl) (contract.TaskReceipt, error) {
	if tc.TaskRef == "" {
		return contract.TaskReceipt{}, fmt.Errorf("%w: task_ref is required", contract.ErrInvalidRequest)
	}
	var rc contract.TaskReceipt
	err := c.c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(tc.TaskRef)+"/"+op, tc, &rc)
	return rc, err
}
