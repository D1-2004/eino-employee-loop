package loop

import (
	"context"
	"database/sql"
	"sync"

	"github.com/dingtalk-fde/eino-employee-loop/store"
)

// ConvStore: conversation log (the "nearby conversation" source). Backed by the same SQL DB.
// Cloud: PostgreSQL table (same DDL) + Redis list as hot cache of the last N messages.
type Msg struct {
	Seq       int64  `json:"seq"`
	Role      string `json:"role"` // user | assistant | host
	Speaker   string `json:"speaker"`
	Text      string `json:"text"`
	Kind      string `json:"kind"` // msg | ack | reply | dispatch_reply | result_reply | imported
	SourceRef string `json:"source_ref"`
	AtMs      int64  `json:"at_ms"` // virtual (frozen-clock) ms
}

type ConvStore struct {
	db  *sql.DB
	pg  bool
	mu  sync.Mutex
	seq map[string]int64
}

const convDDL = `CREATE TABLE IF NOT EXISTS conversation(tenant TEXT NOT NULL, scene TEXT NOT NULL, seq BIGINT NOT NULL, role TEXT NOT NULL, speaker TEXT, text TEXT NOT NULL, kind TEXT, source_ref TEXT, at_ms BIGINT, PRIMARY KEY(tenant,scene,seq))`

func NewConvStore(s *store.SQLStore, pg bool) (*ConvStore, error) {
	_, err := s.DB().Exec(convDDL)
	return &ConvStore{db: s.DB(), pg: pg, seq: map[string]int64{}}, err
}
func (c *ConvStore) q(s string) string {
	if !c.pg {
		return s
	}
	n := 0
	out := []byte{}
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			n++
			out = append(out, []byte("$"+itoa(n))...)
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
func (c *ConvStore) Append(ctx context.Context, tenant, scene string, m Msg) (Msg, error) {
	c.mu.Lock()
	k := tenant + "|" + scene
	if _, ok := c.seq[k]; !ok {
		var mx sql.NullInt64
		_ = c.db.QueryRowContext(ctx, c.q(`SELECT MAX(seq) FROM conversation WHERE tenant=? AND scene=?`), tenant, scene).Scan(&mx)
		c.seq[k] = mx.Int64
	}
	c.seq[k]++
	m.Seq = c.seq[k]
	c.mu.Unlock()
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO conversation(tenant,scene,seq,role,speaker,text,kind,source_ref,at_ms) VALUES(?,?,?,?,?,?,?,?,?)`), tenant, scene, m.Seq, m.Role, m.Speaker, m.Text, m.Kind, m.SourceRef, m.AtMs)
	return m, err
}

// Recent returns up to n newest messages with seq < beforeSeq (0 = no bound), oldest first.
func (c *ConvStore) Recent(ctx context.Context, tenant, scene string, beforeSeq int64, n int) ([]Msg, error) {
	if beforeSeq == 0 {
		beforeSeq = 1 << 60
	}
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT seq,role,COALESCE(speaker,''),text,COALESCE(kind,''),COALESCE(source_ref,''),COALESCE(at_ms,0) FROM conversation WHERE tenant=? AND scene=? AND seq<? ORDER BY seq DESC LIMIT ?`), tenant, scene, beforeSeq, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Msg
	for rows.Next() {
		var m Msg
		if err := rows.Scan(&m.Seq, &m.Role, &m.Speaker, &m.Text, &m.Kind, &m.SourceRef, &m.AtMs); err != nil {
			return nil, err
		}
		out = append([]Msg{m}, out...)
	}
	return out, rows.Err()
}
