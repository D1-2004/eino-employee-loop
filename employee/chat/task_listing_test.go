package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/memory"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func TestFindTasksPagesSummariesAndKeepsOriginalRead(t *testing.T) {
	ctx := context.Background()
	db := memory.New()
	defer db.Close()
	tasks, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	profile := contract.Profile{Workspace: "w", Tenant: "t", Employee: "e", Owner: "human", Scenes: []string{"dm"}}
	scope := profile.Scope("dm")
	original := strings.Repeat("long original goal", 200)
	for i := 0; i < 14; i++ {
		if _, err = tasks.Dispatch(ctx, scope, work.Source{Receipt: fmt.Sprint(i), Actor: "human", Text: "original"}, "dispatch", work.Definition{Goal: original, Instruction: "Preserve original instruction", MaxRuns: 4}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{work: tasks}
	tools, _ := s.chatTools(Job{Profile: profile, Input: contract.Input{Scene: "dm", ID: "current", Actor: "human", Text: "read tasks"}}, "job")
	for _, tool := range tools {
		if tool.Spec.Name == "find_tasks" {
			value, err := tool.Invoke(ctx, "find", map[string]any{"source_ref": "current"})
			if err != nil {
				t.Fatal(err)
			}
			result := value.(map[string]any)
			summaries := result["tasks"].([]map[string]any)
			if len(summaries) != 12 || result["has_more"] != true || result["truncated"] != true {
				t.Fatal(result)
			}
			id := summaries[0]["id"].(string)
			if _, found := summaries[0]["goal"]; found {
				t.Fatal("oversize original leaked into summary", summaries[0])
			}
			task, err := tasks.Get(ctx, scope, id)
			if err != nil || task.Definition.Goal != original || task.Definition.Instruction != "Preserve original instruction" {
				t.Fatal(task, err)
			}
			value, err = tool.Invoke(ctx, "find-next", map[string]any{"offset": float64(12), "source_ref": "current"})
			if err != nil {
				t.Fatal(err)
			}
			result = value.(map[string]any)
			if len(result["tasks"].([]map[string]any)) != 2 || result["has_more"] != false {
				t.Fatal(result)
			}
		}
	}
}
