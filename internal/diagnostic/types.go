package diagnostic

type ComponentType string

const (
	ComponentKubectlAI    ComponentType = "kubectl_ai"
	ComponentLogAnalyzer  ComponentType = "log_analyzer"
	ComponentTroubleshoot ComponentType = "trouble_shooting"
)

type DetectionSource string

const (
	DetectionSourceKubectlOutput DetectionSource = "kubectl_output"
	DetectionSourceAgentText     DetectionSource = "agent_text"
	DetectionSourceToolResult    DetectionSource = "tool_result"
	DetectionSourceLogPattern    DetectionSource = "log_pattern"
	DetectionSourceMetricPattern DetectionSource = "metric_pattern"
	DetectionSourceEvent         DetectionSource = "event"
	DetectionSourceUserQuery     DetectionSource = "user_query"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type ConfidenceLevel string

const (
	ConfidenceCertain   ConfidenceLevel = "certain"
	ConfidenceHigh      ConfidenceLevel = "high"
	ConfidenceMedium    ConfidenceLevel = "medium"
	ConfidenceLow       ConfidenceLevel = "low"
	ConfidenceSpeculate ConfidenceLevel = "speculate"
)

type DetectionType string

const (
	DetectionCrashLoopBackOff  DetectionType = "CrashLoopBackOff"
	DetectionOOMKilled         DetectionType = "OOMKilled"
	DetectionImagePullBackOff  DetectionType = "ImagePullBackOff"
	DetectionErrImagePull      DetectionType = "ErrImagePull"
	DetectionPending           DetectionType = "Pending"
	DetectionFailedScheduling  DetectionType = "FailedScheduling"
	DetectionProbeFailed       DetectionType = "ProbeFailed"
	DetectionServiceNoEndpoint DetectionType = "ServiceNoEndpoint"
	DetectionNetworkFailure    DetectionType = "NetworkFailure"
	DetectionTimeout           DetectionType = "Timeout"
	DetectionDiskFull          DetectionType = "DiskFull"
	DetectionPermissionDenied  DetectionType = "PermissionDenied"
	DetectionConfigError       DetectionType = "ConfigError"
	DetectionUnknown           DetectionType = "Unknown"
)

type KubernetesTarget struct {
	Cluster   string `json:"cluster,omitempty" yaml:"cluster,omitempty"`
	Context   string `json:"context,omitempty" yaml:"context,omitempty"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	PodName   string `json:"pod_name,omitempty" yaml:"pod_name,omitempty"`
	Container string `json:"container,omitempty" yaml:"container,omitempty"`
	OwnerKind string `json:"owner_kind,omitempty" yaml:"owner_kind,omitempty"`
	OwnerName string `json:"owner_name,omitempty" yaml:"owner_name,omitempty"`
}

type Evidence struct {
	Source    DetectionSource `json:"source" yaml:"source"`
	Message   string          `json:"message" yaml:"message"`
	Timestamp string          `json:"timestamp,omitempty" yaml:"timestamp,omitempty"`
	Command   string          `json:"command,omitempty" yaml:"command,omitempty"`
	Query     string          `json:"query,omitempty" yaml:"query,omitempty"`
	RefID     string          `json:"ref_id,omitempty" yaml:"ref_id,omitempty"`
}

type ProblemSignal struct {
	ID             string            `json:"id,omitempty" yaml:"id,omitempty"`
	Source         DetectionSource   `json:"source,omitempty" yaml:"source,omitempty"`
	DetectedBy     ComponentType     `json:"detected_by,omitempty" yaml:"detected_by,omitempty"`
	DetectionTypes []DetectionType   `json:"detection_types,omitempty" yaml:"detection_types,omitempty"`
	Severity       Severity          `json:"severity,omitempty" yaml:"severity,omitempty"`
	Confidence     ConfidenceLevel   `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Summary        string            `json:"summary,omitempty" yaml:"summary,omitempty"`
	Evidence       []Evidence        `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Target         KubernetesTarget  `json:"target,omitempty" yaml:"target,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty" yaml:"attributes,omitempty"`
}
