package ledger

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"time"
)

// ExportEntry is the stable, human-readable JSON shape of a ledger entry.
// Money remains a decimal string so no serializer can lose nanodollar
// precision.
type ExportEntry struct {
	Sequence     int64     `json:"sequence"`
	RunID        string    `json:"run_id"`
	PrincipalID  string    `json:"principal_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      string    `json:"cost_usd"`
	Estimated    bool      `json:"estimated"`
	CreatedAt    time.Time `json:"created_at"`
	PrevHash     string    `json:"prev_hash"`
	Hash         string    `json:"hash"`
}

// ExportEntries converts internal entries to the stable export schema.
func ExportEntries(entries []Entry) []ExportEntry {
	out := make([]ExportEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ExportEntry{
			Sequence: entry.Seq, RunID: entry.RunID, PrincipalID: entry.PrincipalID,
			Provider: entry.Provider, Model: entry.Model, InputTokens: entry.InputTokens,
			OutputTokens: entry.OutputTokens, CostUSD: entry.Cost.String(), Estimated: entry.Estimated,
			CreatedAt: entry.CreatedAt.UTC(), PrevHash: entry.PrevHash, Hash: entry.Hash,
		})
	}
	return out
}

// WriteJSON writes a versioned JSON object containing the supplied entries.
func WriteJSON(w io.Writer, entries []Entry) error {
	return json.NewEncoder(w).Encode(struct {
		Version int           `json:"version"`
		Entries []ExportEntry `json:"entries"`
	}{Version: 1, Entries: ExportEntries(entries)})
}

// WriteCSV writes the stable ledger CSV header and all supplied entries.
func WriteCSV(w io.Writer, entries []Entry) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"sequence", "run_id", "principal_id", "provider", "model", "input_tokens", "output_tokens", "cost_usd", "estimated", "created_at", "prev_hash", "hash"}); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := cw.Write([]string{
			strconv.FormatInt(entry.Seq, 10), entry.RunID, entry.PrincipalID, entry.Provider, entry.Model,
			strconv.FormatInt(entry.InputTokens, 10), strconv.FormatInt(entry.OutputTokens, 10),
			entry.Cost.String(), strconv.FormatBool(entry.Estimated), entry.CreatedAt.UTC().Format(time.RFC3339Nano),
			entry.PrevHash, entry.Hash,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
