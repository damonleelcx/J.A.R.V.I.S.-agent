package looks

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteAudit keeps every verdict beside the exact images that produced it.
//
// # Why the images are written out and not just referenced
//
// A verdict from a vision model is only worth what a person can check, and the
// thing to check is whether the picture actually shows what the sentence claims.
// Recording "the judge said the new one is better" without the two pictures
// leaves a reader with a claim and no way to disagree with it — which is the
// shape of the failure look.go's note describes, where a swallowed error read as
// a clean bill of health. So each ask's two images are written as files in the
// order they were sent, the answer is kept verbatim beside them, and the ONLY
// thing WriteAudit invents is the file names.
//
// The data URIs are also stripped out of the JSON: a verdicts.json with four
// base64 PNGs inline is a file nobody opens, and a repository nobody wants.
func WriteAudit(dir string, d Decision) error {
	into := filepath.Join(dir, safeName(d.Change.ID))
	if err := os.MkdirAll(into, 0o755); err != nil {
		return err
	}

	record := auditRecord{
		ID: d.Change.ID, What: d.Change.What, Verdict: d.Verdict, Accepted: d.Accepted,
		Agreed: d.Agreed, Reason: d.Reason, Tokens: d.Tokens,
		Check: auditCheck{
			Passes:  d.Guard.OK,
			Rose:    d.Guard.Rose,
			Faults:  fmt.Sprintf("%d→%d", d.Guard.FaultsBefore, d.Guard.FaultsAfter),
			Buried:  fmt.Sprintf("%d→%d", d.Guard.BuriedBefore, d.Guard.BuriedAfter),
			Skipped: fmt.Sprintf("%d→%d", d.Guard.SkippedBefore, d.Guard.SkippedAfter),
		},
	}

	for i, r := range d.Rounds {
		a := auditRound{
			N: i + 1, View: r.View, Raw: r.Raw, Why: r.Why,
			SaidAboutTheNewOne: string(r.Said), Tokens: r.Tokens,
		}
		a.Order = "image 1 is the OLD render, image 2 is the NEW one"
		if r.NewIsFirst {
			a.Order = "image 1 is the NEW render, image 2 is the OLD one"
		}
		for k, img := range r.Images {
			name := fmt.Sprintf("round%d-%s-image%d.png", i+1, r.View, k+1)
			if err := writeImage(filepath.Join(into, name), img); err != nil {
				// An image that could not be written is said so IN the record
				// rather than dropped: a round with a missing picture must not
				// read as a round that had none.
				a.Images = append(a.Images, "NOT WRITTEN ("+err.Error()+")")
				continue
			}
			a.Images = append(a.Images, name)
		}
		record.Rounds = append(record.Rounds, a)
	}

	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(into, "verdict.json"), append(raw, '\n'), 0o644)
}

// auditRecord is one decision on disk.
type auditRecord struct {
	ID       string       `json:"id"`
	What     string       `json:"what"`
	Verdict  Verdict      `json:"verdict"`
	Accepted bool         `json:"accepted"`
	Agreed   bool         `json:"both_ways_agreed"`
	Reason   string       `json:"reason"`
	Tokens   int64        `json:"tokens"`
	Check    auditCheck   `json:"check"`
	Rounds   []auditRound `json:"rounds"`
}

// auditCheck is what the DEFECT check said, both sides, as a reader reads it.
// Written as "before→after" rather than as two numbers so nobody has to work out
// which column is which.
type auditCheck struct {
	Passes  bool     `json:"passes"`
	Rose    []string `json:"rose,omitempty"`
	Faults  string   `json:"faults"`
	Buried  string   `json:"buried_pairs"`
	Skipped string   `json:"skipped_parts"`
}

// auditRound is one ask as the record keeps it: the files, in the order they
// were sent, and the answer word for word.
type auditRound struct {
	N                  int      `json:"n"`
	View               string   `json:"view"`
	Order              string   `json:"order"`
	Images             []string `json:"images"`
	Raw                string   `json:"answer_verbatim"`
	Why                string   `json:"why"`
	SaidAboutTheNewOne string   `json:"said_about_the_new_one"`
	Tokens             int64    `json:"tokens"`
}

// writeImage turns a PNG data URI back into a file.
func writeImage(path, dataURI string) error {
	_, payload, found := strings.Cut(dataURI, ",")
	if !found {
		return fmt.Errorf("not a data URI")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// safeName keeps a change id from reaching outside the audit directory.
func safeName(id string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, id)
	if out == "" {
		return "unnamed"
	}
	return out
}
