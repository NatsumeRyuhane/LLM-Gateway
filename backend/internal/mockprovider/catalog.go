package mockprovider

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const MatrixSchemaVersion = "gateway.mock-provider.matrix.v0"

//go:embed fixtures/v0/matrix.json
var embeddedMatrix []byte

// Mode identifies which Chat Completions response mode a profile accepts.
type Mode string

const (
	ModeBuffered  Mode = "buffered"
	ModeStreaming Mode = "streaming"
	ModeEither    Mode = "either"
	ModeExternal  Mode = "external"
)

// InjectionLayer identifies the component capable of reproducing a fault.
type InjectionLayer string

const (
	LayerProviderHandler InjectionLayer = "provider_handler"
	LayerTransport       InjectionLayer = "transport_harness"
	LayerGatewayConsumer InjectionLayer = "gateway_consumer"
	LayerDownstream      InjectionLayer = "downstream_client"
)

// Behavior is the bounded fixture behavior selected by a profile.
type Behavior struct {
	Kind                  string   `json:"kind"`
	Status                int      `json:"status,omitempty"`
	RetryAfter            string   `json:"retry_after,omitempty"`
	Bytes                 int      `json:"bytes,omitempty"`
	FailuresBeforeSuccess int      `json:"failures_before_success,omitempty"`
	Steps                 []string `json:"steps,omitempty"`
}

// Expected records the current immediate gateway classification for a profile.
type Expected struct {
	FailureCode      string `json:"failure_code"`
	Domain           string `json:"domain"`
	RetryDisposition string `json:"retry_disposition"`
	ProviderStatus   int    `json:"provider_status"`
	OutputVisible    bool   `json:"output_visible"`
	ToolActionable   bool   `json:"tool_actionable"`
	Terminal         string `json:"terminal"`
}

// Profile is one immutable versioned fault-matrix row.
type Profile struct {
	ID                    string         `json:"id"`
	Mode                  Mode           `json:"mode"`
	Seed                  int64          `json:"seed"`
	InjectionLayer        InjectionLayer `json:"injection_layer"`
	Behavior              Behavior       `json:"behavior"`
	SynchronizationEvents []Event        `json:"synchronization_events"`
	Expected              Expected       `json:"expected"`
	GroundTruth           string         `json:"ground_truth"`
	DetectionOwner        string         `json:"detection_owner"`
	Evidence              []string       `json:"evidence"`
}

type matrixDocument struct {
	SchemaVersion  string    `json:"schema_version"`
	DefaultProfile string    `json:"default_profile"`
	Profiles       []Profile `json:"profiles"`
}

// Catalog is an immutable snapshot of the embedded v0 matrix.
type Catalog struct {
	schemaVersion  string
	defaultProfile string
	profiles       map[string]Profile
}

// LoadCatalog parses and validates a fresh immutable catalog snapshot.
func LoadCatalog() (Catalog, error) {
	var document matrixDocument
	decoder := json.NewDecoder(bytesReader(embeddedMatrix))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Catalog{}, fmt.Errorf("decode mock-provider matrix: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Catalog{}, fmt.Errorf("decode mock-provider matrix: %w", err)
	}
	if document.SchemaVersion != MatrixSchemaVersion {
		return Catalog{}, fmt.Errorf("mock-provider matrix schema %q is unsupported", document.SchemaVersion)
	}
	if len(document.Profiles) == 0 {
		return Catalog{}, errors.New("mock-provider matrix must contain profiles")
	}
	profiles := make(map[string]Profile, len(document.Profiles))
	for index, profile := range document.Profiles {
		if err := validateProfile(profile); err != nil {
			return Catalog{}, fmt.Errorf("mock-provider profile %d: %w", index, err)
		}
		if _, duplicate := profiles[profile.ID]; duplicate {
			return Catalog{}, fmt.Errorf("mock-provider profile %q is duplicated", profile.ID)
		}
		profiles[profile.ID] = cloneProfile(profile)
	}
	if _, ok := profiles[document.DefaultProfile]; !ok {
		return Catalog{}, errors.New("mock-provider default profile is missing")
	}
	return Catalog{schemaVersion: document.SchemaVersion, defaultProfile: document.DefaultProfile, profiles: profiles}, nil
}

// SchemaVersion returns the immutable matrix schema version.
func (c Catalog) SchemaVersion() string { return c.schemaVersion }

// DefaultProfile returns the profile selected when no explicit ID is given.
func (c Catalog) DefaultProfile() string { return c.defaultProfile }

// Profile returns an isolated copy of one profile.
func (c Catalog) Profile(id string) (Profile, bool) {
	profile, ok := c.profiles[id]
	return cloneProfile(profile), ok
}

// Profiles returns isolated profile copies in stable identifier order.
func (c Catalog) Profiles() []Profile {
	profiles := make([]Profile, 0, len(c.profiles))
	for _, profile := range c.profiles {
		profiles = append(profiles, cloneProfile(profile))
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return profiles
}

func validateProfile(profile Profile) error {
	if !validToken(profile.ID, 128) {
		return errors.New("ID is invalid")
	}
	switch profile.Mode {
	case ModeBuffered, ModeStreaming, ModeEither, ModeExternal:
	default:
		return errors.New("mode is invalid")
	}
	switch profile.InjectionLayer {
	case LayerProviderHandler, LayerTransport, LayerGatewayConsumer, LayerDownstream:
	default:
		return errors.New("injection layer is invalid")
	}
	if !validToken(profile.Behavior.Kind, 64) || !validToken(profile.GroundTruth, 128) {
		return errors.New("behavior or ground-truth label is invalid")
	}
	if profile.Expected.FailureCode == "" {
		if profile.Expected.Domain != "" || profile.Expected.RetryDisposition != "" {
			return errors.New("successful expectation cannot contain failure metadata")
		}
	} else if !validToken(profile.Expected.FailureCode, 128) || !validToken(profile.Expected.Domain, 32) || !validToken(profile.Expected.RetryDisposition, 64) {
		return errors.New("failure expectation is invalid")
	}
	seen := make(map[Event]struct{}, len(profile.SynchronizationEvents))
	for _, event := range profile.SynchronizationEvents {
		if !event.valid() {
			return errors.New("synchronization event is invalid")
		}
		if _, duplicate := seen[event]; duplicate {
			return errors.New("synchronization event is duplicated")
		}
		seen[event] = struct{}{}
	}
	return nil
}

func cloneProfile(profile Profile) Profile {
	profile.Behavior.Steps = append([]string(nil), profile.Behavior.Steps...)
	profile.SynchronizationEvents = append([]Event(nil), profile.SynchronizationEvents...)
	profile.Evidence = append([]string(nil), profile.Evidence...)
	return profile
}

func validToken(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
