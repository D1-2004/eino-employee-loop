package work

import "context"

type DeliveryFact struct {
	IntentID, ProviderID, BodySHA256, Kind string
	GoalRevision                           uint64
}

func (m *Manager) RecordDelivery(ctx context.Context, scope Scope, taskID string, fact DeliveryFact) (Receipt, error) {
	if fact.IntentID == "" || fact.ProviderID == "" || fact.BodySHA256 == "" || fact.GoalRevision == 0 {
		return Receipt{}, ErrState
	}
	return m.apply(ctx, scope, stableID("delivery-", []any{scope, taskID, fact.IntentID}), fact, taskID, false, func(t *Task) error {
		if fact.GoalRevision > t.Goal.Revision {
			return ErrStale
		}
		t.Deliveries = append(t.Deliveries, fact)
		// An outstanding explicit delivery Wait is awakened by the actual receipt.
		// Historical receipts cannot resolve a later Goal's Wait.
		if t.State == Waiting && fact.GoalRevision == t.Goal.Revision {
			for i := range t.Waits {
				w := &t.Waits[i]
				if !w.Resolved && w.Kind == "event" && w.Condition == "delivery/"+fact.IntentID {
					w.Answer = fact.ProviderID
					w.SourceReceipt = "delivery/" + fact.IntentID
					w.Resolved = true
				}
			}
			all := true
			for _, w := range t.Waits {
				if !w.Resolved {
					all = false
				}
			}
			if all {
				t.State, t.Goal.State = Ready, Ready
				t.NeedsReview = true
			}
		}
		return nil
	})
}
