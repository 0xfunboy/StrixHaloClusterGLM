package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const sharedComputeSchema = "strix-cluster-compute-v2"

type sharedComputeRank struct {
	Unit         string `json:"unit"`
	InvocationID string `json:"invocation_id"`
	Nonce        string `json:"nonce"`
}

type sharedComputeReceipt struct {
	Schema  string                       `json:"schema"`
	Owner   string                       `json:"owner"`
	State   string                       `json:"state"`
	Epoch   string                       `json:"epoch"`
	Detail  string                       `json:"detail,omitempty"`
	Updated string                       `json:"updated"`
	Ranks   map[string]sharedComputeRank `json:"ranks"`
}

func sharedComputePath(dir string) string     { return filepath.Join(dir, "owner.json") }
func sharedComputeLockPath(dir string) string { return filepath.Join(dir, "compute.lock") }

func readSharedCompute(dir string) (sharedComputeReceipt, error) {
	var r sharedComputeReceipt
	if dir == "" {
		return r, errors.New("shared compute state is not configured")
	}
	b, e := os.ReadFile(sharedComputePath(dir))
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.Schema != sharedComputeSchema || r.Owner == "" || r.State == "" || r.Ranks == nil {
		return r, errors.New("invalid shared compute receipt")
	}
	return r, nil
}

func writeSharedCompute(dir string, r sharedComputeReceipt) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("invalid shared compute state directory")
	}
	if r.Schema == "" {
		r.Schema = sharedComputeSchema
	}
	if r.Schema != sharedComputeSchema {
		return errors.New("invalid shared compute schema")
	}
	if r.Owner != "NONE" && r.Owner != "GLM" && r.Owner != "DS41" {
		return fmt.Errorf("invalid shared compute owner %q", r.Owner)
	}
	switch r.State {
	case "OFF", "STARTING", "RUNNING", "READY", "STOPPING", "UNRECONCILED":
	default:
		return fmt.Errorf("invalid shared compute state %q", r.State)
	}
	if r.Ranks == nil {
		r.Ranks = map[string]sharedComputeRank{}
	}
	r.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	return controllerWrite(sharedComputePath(dir), r)
}

func sharedFromGLM(state string, o ClusterOwner, detail string) sharedComputeReceipt {
	r := sharedComputeReceipt{Schema: sharedComputeSchema, Owner: "GLM", State: state, Epoch: o.Epoch, Detail: detail, Ranks: map[string]sharedComputeRank{}}
	for _, u := range o.Units {
		r.Ranks[fmt.Sprintf("%d", u.Rank)] = sharedComputeRank{Unit: u.Name + ".service", InvocationID: u.InvocationID, Nonce: u.Nonce}
	}
	return r
}

func sharedOff(detail string) sharedComputeReceipt {
	return sharedComputeReceipt{Schema: sharedComputeSchema, Owner: "NONE", State: "OFF", Detail: detail, Ranks: map[string]sharedComputeRank{}}
}

func sharedStartAllowed(r sharedComputeReceipt) error {
	if r.Schema != sharedComputeSchema || r.Owner != "NONE" || r.State != "OFF" {
		return fmt.Errorf("cluster not reconciled OFF: owner=%s state=%s", r.Owner, r.State)
	}
	return nil
}

func sharedMatchesGLM(r sharedComputeReceipt, o ClusterOwner) error {
	if r.Schema != sharedComputeSchema || r.Owner != "GLM" || r.Epoch != o.Epoch {
		return fmt.Errorf("shared GLM receipt mismatch: owner=%s state=%s epoch=%s want=%s", r.Owner, r.State, r.Epoch, o.Epoch)
	}
	for _, u := range o.Units {
		got, ok := r.Ranks[fmt.Sprintf("%d", u.Rank)]
		if !ok || got.Unit != u.Name+".service" || got.InvocationID != u.InvocationID || got.Nonce != u.Nonce {
			return fmt.Errorf("shared GLM rank%d identity mismatch", u.Rank)
		}
	}
	return nil
}
