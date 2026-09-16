package repository

import (
	"testing"
	"time"
)

// nullJSONScanner stands in for a row whose jsonb columns are NULL, which is the
// state of every freshly queued test before a result exists.
type nullJSONScanner struct {
	cfgNull  bool
	evalNull bool
}

func (s nullJSONScanner) Scan(dest ...any) error {
	*dest[0].(*int64) = 41
	*dest[1].(*int64) = 9001
	*dest[2].(*string) = "degradation_probe"
	*dest[3].(*string) = "queued"
	*dest[4].(**float64) = nil
	*dest[5].(*string) = ""
	*dest[6].(*string) = ""
	*dest[7].(*string) = "prompt"
	*dest[8].(*string) = ""
	*dest[9].(*bool) = false
	*dest[10].(*string) = ""
	*dest[11].(*int64) = 0
	*dest[12].(*string) = "gpt-6-astra"
	*dest[13].(*bool) = false
	if !s.cfgNull {
		*dest[14].(*[]byte) = []byte(`{"prompt":"p","model":"gpt-6-astra","reasoning_effort":"medium","evaluator":"exact_answer","expected_answer":"21","timeout_seconds":300}`)
	}
	if !s.evalNull {
		*dest[15].(*[]byte) = []byte(`{"answer_verdict":"correct"}`)
	}
	*dest[16].(**time.Time) = nil
	*dest[17].(**time.Time) = nil
	*dest[18].(*time.Time) = time.Now()
	*dest[19].(*string) = "lease"
	*dest[20].(*string) = ""
	*dest[21].(**time.Time) = nil
	return nil
}

// A queued row has no evaluation yet. Scanning it must not fail: before this
// guard every claim died with "unexpected end of JSON input" and the merged
// intelligent-test feature could never execute anything.
func TestScanIntelligentRecordToleratesNullJSONColumns(t *testing.T) {
	record, err := scanIntelligentRecord(nullJSONScanner{evalNull: true, cfgNull: true})
	if err != nil {
		t.Fatalf("a NULL evaluation must scan cleanly, got %v", err)
	}
	if record == nil || record.ID != 41 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.Evaluation == nil {
		t.Fatal("a NULL evaluation must normalize to an empty map, not nil")
	}
	if len(record.Evaluation) != 0 {
		t.Fatalf("expected an empty evaluation, got %v", record.Evaluation)
	}
	if record.ConfigSnapshot == nil || record.ConfigSnapshot.Model != "" {
		t.Fatalf("a NULL config snapshot must stay a usable empty struct, got %+v", record.ConfigSnapshot)
	}
}

func TestScanIntelligentRecordStillParsesPresentJSON(t *testing.T) {
	record, err := scanIntelligentRecord(nullJSONScanner{})
	if err != nil {
		t.Fatalf("a fully populated row must scan: %v", err)
	}
	if record.ConfigSnapshot.Model != "gpt-6-astra" || record.ConfigSnapshot.ReasoningEffort != "medium" {
		t.Fatalf("config snapshot lost data: %+v", record.ConfigSnapshot)
	}
	if record.Evaluation["answer_verdict"] != "correct" {
		t.Fatalf("evaluation lost data: %+v", record.Evaluation)
	}
}
