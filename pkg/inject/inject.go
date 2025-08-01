package inject

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonfilter "github.com/clarketm/json"
	"github.com/linkerd/linkerd2/charts"
	chartspkg "github.com/linkerd/linkerd2/pkg/charts"
	l5dcharts "github.com/linkerd/linkerd2/pkg/charts/linkerd2"
	"github.com/linkerd/linkerd2/pkg/k8s"
	"github.com/linkerd/linkerd2/pkg/util"
	log "github.com/sirupsen/logrus"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8sResource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

var (
	rTrail = regexp.MustCompile(`\},\s*\]`)

	// ProxyAnnotations is the list of possible annotations that can be applied on a pod or namespace.
	// All these annotations should be prefixed with "config.linkerd.io"
	ProxyAnnotations = []string{
		k8s.ProxyAdminPortAnnotation,
		k8s.ProxyControlPortAnnotation,
		k8s.ProxyEnableDebugAnnotation,
		k8s.ProxyEnableExternalProfilesAnnotation,
		k8s.ProxyImagePullPolicyAnnotation,
		k8s.ProxyInboundPortAnnotation,
		k8s.ProxyInitImageAnnotation,
		k8s.ProxyInitImageVersionAnnotation,
		k8s.ProxyOutboundPortAnnotation,
		k8s.ProxyPodInboundPortsAnnotation,
		k8s.ProxyCPULimitAnnotation,
		k8s.ProxyCPURequestAnnotation,
		k8s.ProxyImageAnnotation,
		k8s.ProxyAdminShutdownAnnotation,
		k8s.ProxyLogFormatAnnotation,
		k8s.ProxyLogLevelAnnotation,
		k8s.ProxyLogHTTPHeaders,
		k8s.ProxyMemoryLimitAnnotation,
		k8s.ProxyMemoryRequestAnnotation,
		k8s.ProxyEphemeralStorageLimitAnnotation,
		k8s.ProxyEphemeralStorageRequestAnnotation,
		k8s.ProxyUIDAnnotation,
		k8s.ProxyGIDAnnotation,
		k8s.ProxyVersionOverrideAnnotation,
		k8s.ProxyRequireIdentityOnInboundPortsAnnotation,
		k8s.ProxyIgnoreInboundPortsAnnotation,
		k8s.ProxyOpaquePortsAnnotation,
		k8s.ProxyIgnoreOutboundPortsAnnotation,
		k8s.ProxyEnableHostnameLabels,
		k8s.ProxyOutboundConnectTimeout,
		k8s.ProxyInboundConnectTimeout,
		k8s.ProxyAwait,
		k8s.ProxyDefaultInboundPolicyAnnotation,
		k8s.ProxySkipSubnetsAnnotation,
		k8s.ProxyAccessLogAnnotation,
		k8s.ProxyShutdownGracePeriodAnnotation,
		k8s.ProxyOutboundDiscoveryCacheUnusedTimeout,
		k8s.ProxyInboundDiscoveryCacheUnusedTimeout,
		k8s.ProxyDisableOutboundProtocolDetectTimeout,
		k8s.ProxyDisableInboundProtocolDetectTimeout,
	}
	// ProxyAlphaConfigAnnotations is the list of all alpha configuration
	// (config.alpha prefix) that can be applied to a pod or namespace.
	ProxyAlphaConfigAnnotations = []string{
		k8s.ProxyWaitBeforeExitSecondsAnnotation,
		k8s.ProxyEnableNativeSidecarAnnotation,
	}
)

// OverriddenValues contains the result of executing an instance of ValueOverrider
type OverriddenValues struct {
	*l5dcharts.Values

	// may contain additional values that are not part of the l5dcharts.Values
	// in order to allow custom ValueOverrider implementations to add their own
	// values to the rendering logic.
	Additional map[string]interface{}
}

// ValueOverrider is used to override the default values that are used in chart rendering based
// on the annotations provided in overrides.
type ValueOverrider func(rc *ResourceConfig) (*OverriddenValues, error)

// Origin defines where the input YAML comes from. Refer the ResourceConfig's
// 'origin' field
type Origin int

const (
	// OriginCLI is the value of the ResourceConfig's 'origin' field if the input
	// YAML comes from the CLI
	OriginCLI Origin = iota

	// OriginWebhook is the value of the ResourceConfig's 'origin' field if the input
	// YAML comes from the CLI
	OriginWebhook

	// OriginUnknown is the value of the ResourceConfig's 'origin' field if the
	// input YAML comes from an unknown source
	OriginUnknown
)

// OwnerRetrieverFunc is a function that returns a pod's owner reference
// kind and name
type OwnerRetrieverFunc func(*corev1.Pod) (string, string, error)

// ResourceConfig contains the parsed information for a given workload
type ResourceConfig struct {
	// These values used for the rendering of the patch may be further
	// overridden by the annotations on the resource or the resource's
	// namespace.
	values *l5dcharts.Values

	namespace string

	// These annotations from the resources's namespace are used as a base.
	// The resources's annotations will be applied on top of these, which
	// allows the nsAnnotations to act as a default.
	nsAnnotations  map[string]string
	ownerRetriever OwnerRetrieverFunc
	origin         Origin

	workload struct {
		obj      runtime.Object
		metaType metav1.TypeMeta
		// Meta is the workload's metadata. It's exported so that metadata of
		// non-workload resources can be unmarshalled by the YAML parser
		Meta     *metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`
		ownerRef *metav1.OwnerReference
	}

	pod struct {
		meta *metav1.ObjectMeta
		// This fields hold labels and annotations which are to be added to the
		// injected resource. This is different from meta.Labels and
		// meta.Annotations which are the labels and annotations on the original
		// resource before injection.
		labels      map[string]string
		annotations map[string]string
		spec        *corev1.PodSpec
	}
}

type podPatch struct {
	l5dcharts.Values
	PathPrefix            string                    `json:"pathPrefix"`
	AddRootMetadata       bool                      `json:"addRootMetadata"`
	AddRootAnnotations    bool                      `json:"addRootAnnotations"`
	Annotations           map[string]string         `json:"annotations"`
	AddRootLabels         bool                      `json:"addRootLabels"`
	AddRootInitContainers bool                      `json:"addRootInitContainers"`
	AddRootVolumes        bool                      `json:"addRootVolumes"`
	Labels                map[string]string         `json:"labels"`
	DebugContainer        *l5dcharts.DebugContainer `json:"debugContainer"`
}

type annotationPatch struct {
	AddRootAnnotations bool
	OpaquePorts        string
}

// AppendNamespaceAnnotations allows pods to inherit config specific annotations
// from the namespace they belong to. If the namespace has a valid config key
// that the pod does not, then it is appended to the pod's template
func AppendNamespaceAnnotations(base map[string]string, nsAnn map[string]string, workloadAnn map[string]string) {
	ann := append(ProxyAnnotations, ProxyAlphaConfigAnnotations...)
	ann = append(ann, k8s.ProxyInjectAnnotation)

	for _, key := range ann {
		if _, found := nsAnn[key]; !found {
			continue
		}
		if val, ok := GetConfigOverride(key, workloadAnn, nsAnn); ok {
			base[key] = val
		}
	}
}

// GetOverriddenValues returns the final Values struct which is created
// by overriding annotated configuration on top of default Values
func GetOverriddenValues(rc *ResourceConfig) (*OverriddenValues, error) {
	// Make a copy of Values and mutate that
	copyValues, err := rc.GetValues().DeepCopy()
	if err != nil {
		return nil, err
	}

	namedPorts := make(map[string]int32)
	if rc.HasPodTemplate() {
		namedPorts = util.GetNamedPorts(rc.pod.spec.Containers)
	}

	ApplyAnnotationOverrides(copyValues, rc.GetAnnotationOverrides(), namedPorts)
	return &OverriddenValues{Values: copyValues}, nil
}

func ApplyAnnotationOverrides(values *l5dcharts.Values, annotations map[string]string, namedPorts map[string]int32) {
	if override, ok := annotations[k8s.ProxyInjectAnnotation]; ok {
		if override == k8s.ProxyInjectIngress {
			values.Proxy.IsIngress = true
		}
	}

	if override, ok := annotations[k8s.ProxyImageAnnotation]; ok {
		values.Proxy.Image.Name = override
	}

	if override, ok := annotations[k8s.ProxyVersionOverrideAnnotation]; ok {
		values.Proxy.Image.Version = override
	}

	if override, ok := annotations[k8s.ProxyImagePullPolicyAnnotation]; ok {
		values.Proxy.Image.PullPolicy = override
	}

	if override, ok := annotations[k8s.ProxyInitImageVersionAnnotation]; ok {
		values.ProxyInit.Image.Version = override
	}

	if override, ok := annotations[k8s.ProxyControlPortAnnotation]; ok {
		controlPort, err := strconv.ParseInt(override, 10, 32)
		if err == nil {
			values.Proxy.Ports.Control = int32(controlPort)
		}
	}

	if override, ok := annotations[k8s.ProxyInboundPortAnnotation]; ok {
		inboundPort, err := strconv.ParseInt(override, 10, 32)
		if err == nil {
			values.Proxy.Ports.Inbound = int32(inboundPort)
		}
	}

	if override, ok := annotations[k8s.ProxyAdminPortAnnotation]; ok {
		adminPort, err := strconv.ParseInt(override, 10, 32)
		if err == nil {
			values.Proxy.Ports.Admin = int32(adminPort)
		}
	}

	if override, ok := annotations[k8s.ProxyOutboundPortAnnotation]; ok {
		outboundPort, err := strconv.ParseInt(override, 10, 32)
		if err == nil {
			values.Proxy.Ports.Outbound = int32(outboundPort)
		}
	}

	if override, ok := annotations[k8s.ProxyPodInboundPortsAnnotation]; ok {
		values.Proxy.PodInboundPorts = override
	}

	if override, ok := annotations[k8s.ProxyAdminShutdownAnnotation]; ok {
		if override == k8s.Enabled || override == k8s.Disabled {
			values.Proxy.EnableShutdownEndpoint = override == k8s.Enabled
		} else {
			log.Warnf("unrecognized value used for the %s annotation, valid values are: [%s, %s]", k8s.ProxyAdminShutdownAnnotation, k8s.Enabled, k8s.Disabled)
		}
	}

	if override, ok := annotations[k8s.ProxyLogLevelAnnotation]; ok {
		values.Proxy.LogLevel = override
	}

	if override, ok := annotations[k8s.ProxyLogHTTPHeaders]; ok {
		values.Proxy.LogHTTPHeaders = override
	}

	if override, ok := annotations[k8s.ProxyLogFormatAnnotation]; ok {
		values.Proxy.LogFormat = override
	}

	if override, ok := annotations[k8s.ProxyRequireIdentityOnInboundPortsAnnotation]; ok {
		values.Proxy.RequireIdentityOnInboundPorts = override
	}

	if override, ok := annotations[k8s.ProxyEnableHostnameLabels]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.Metrics.HostnameLabels = value
		}
	}

	if override, ok := annotations[k8s.ProxyOutboundConnectTimeout]; ok {
		duration, err := time.ParseDuration(override)
		if err != nil {
			log.Warnf("unrecognized proxy-outbound-connect-timeout duration value found on pod annotation: %s", err.Error())
		} else {
			values.Proxy.OutboundConnectTimeout = fmt.Sprintf("%dms", int(duration.Seconds()*1000))
		}
	}

	if override, ok := annotations[k8s.ProxyInboundConnectTimeout]; ok {
		duration, err := time.ParseDuration(override)
		if err != nil {
			log.Warnf("unrecognized proxy-inbound-connect-timeout duration value found on pod annotation: %s", err.Error())
		} else {
			values.Proxy.InboundConnectTimeout = fmt.Sprintf("%dms", int(duration.Seconds()*1000))
		}
	}

	if override, ok := annotations[k8s.ProxyOutboundDiscoveryCacheUnusedTimeout]; ok {
		duration, err := time.ParseDuration(override)
		if err != nil {
			log.Warnf("unrecognized duration value used on pod annotation %s: %s", k8s.ProxyOutboundDiscoveryCacheUnusedTimeout, err.Error())
		} else {
			values.Proxy.OutboundDiscoveryCacheUnusedTimeout = fmt.Sprintf("%ds", int(duration.Seconds()))
		}
	}

	if override, ok := annotations[k8s.ProxyInboundDiscoveryCacheUnusedTimeout]; ok {
		duration, err := time.ParseDuration(override)
		if err != nil {
			log.Warnf("unrecognized duration value used on pod annotation %s: %s", k8s.ProxyInboundDiscoveryCacheUnusedTimeout, err.Error())
		} else {
			values.Proxy.InboundDiscoveryCacheUnusedTimeout = fmt.Sprintf("%ds", int(duration.Seconds()))
		}
	}

	if override, ok := annotations[k8s.ProxyDisableOutboundProtocolDetectTimeout]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.DisableOutboundProtocolDetectTimeout = value
		} else {
			log.Warnf("unrecognised value used on pod annotation %s: %s", k8s.ProxyDisableOutboundProtocolDetectTimeout, err.Error())
		}
	}

	if override, ok := annotations[k8s.ProxyDisableInboundProtocolDetectTimeout]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.DisableInboundProtocolDetectTimeout = value
		} else {
			log.Warnf("unrecognised value used on pod annotation %s: %s", k8s.ProxyDisableInboundProtocolDetectTimeout, err.Error())
		}
	}

	if override, ok := annotations[k8s.ProxyShutdownGracePeriodAnnotation]; ok {
		duration, err := time.ParseDuration(override)
		if err != nil {
			log.Warnf("unrecognized proxy-shutdown-grace-period duration value found on pod annotation: %s", err.Error())
		} else {
			values.Proxy.ShutdownGracePeriod = fmt.Sprintf("%dms", int(duration.Seconds()*1000))
		}
	}

	if override, ok := annotations[k8s.ProxyEnableGatewayAnnotation]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.IsGateway = value
		}
	}

	if override, ok := annotations[k8s.ProxyWaitBeforeExitSecondsAnnotation]; ok {
		waitBeforeExitSeconds, err := strconv.ParseUint(override, 10, 64)
		if nil != err {
			log.Warnf("unrecognized value used for the %s annotation, uint64 is expected: %s",
				k8s.ProxyWaitBeforeExitSecondsAnnotation, override)
		} else {
			values.Proxy.WaitBeforeExitSeconds = waitBeforeExitSeconds
		}
	}

	if override, ok := annotations[k8s.ProxyEnableNativeSidecarAnnotation]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.NativeSidecar = value
		}
	}

	// Proxy CPU resources

	if override, ok := annotations[k8s.ProxyCPURequestAnnotation]; ok {
		q, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyCPURequestAnnotation)
		} else {
			values.Proxy.Resources.CPU.Request = override

			n, err := ToWholeCPUCores(q)
			if err != nil {
				log.Warnf("%s (%s)", err, k8s.ProxyCPULimitAnnotation)
			}
			values.Proxy.Runtime.Workers.Minimum = n
		}
	}

	if override, ok := annotations[k8s.ProxyCPULimitAnnotation]; ok {
		q, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyCPULimitAnnotation)
		} else {
			values.Proxy.Resources.CPU.Limit = override

			n, err := ToWholeCPUCores(q)
			if err != nil {
				log.Warnf("%s (%s)", err, k8s.ProxyCPULimitAnnotation)
			}
			values.Proxy.Runtime.Workers.Maximum = n
		}
	}

	if override, ok := annotations[k8s.ProxyCPURatioLimitAnnotation]; ok {
		ratio, err := strconv.ParseFloat(override, 64)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyCPURatioLimitAnnotation)
		} else if (ratio <= 0.0) || (ratio >= 1.0) {
			log.Warnf("invalid value used for the %s annotation, valid values are between 0.0 and 1.0",
				k8s.ProxyCPURatioLimitAnnotation)
		} else {
			values.Proxy.Runtime.Workers.MaximumCPURatio = ratio
		}
	}

	// Proxy memory resources

	if override, ok := annotations[k8s.ProxyMemoryRequestAnnotation]; ok {
		_, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyMemoryRequestAnnotation)
		} else {
			values.Proxy.Resources.Memory.Request = override
		}
	}

	if override, ok := annotations[k8s.ProxyMemoryLimitAnnotation]; ok {
		_, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyMemoryLimitAnnotation)
		} else {
			values.Proxy.Resources.Memory.Limit = override
		}
	}

	// Proxy ephemeral storage resources

	if override, ok := annotations[k8s.ProxyEphemeralStorageRequestAnnotation]; ok {
		_, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyEphemeralStorageRequestAnnotation)
		} else {
			values.Proxy.Resources.EphemeralStorage.Request = override
		}
	}

	if override, ok := annotations[k8s.ProxyEphemeralStorageLimitAnnotation]; ok {
		_, err := k8sResource.ParseQuantity(override)
		if err != nil {
			log.Warnf("%s (%s)", err, k8s.ProxyEphemeralStorageLimitAnnotation)
		} else {
			values.Proxy.Resources.EphemeralStorage.Limit = override
		}
	}

	if override, ok := annotations[k8s.ProxyUIDAnnotation]; ok {
		v, err := strconv.ParseInt(override, 10, 64)
		if err == nil {
			values.Proxy.UID = v
		}
	}

	if override, ok := annotations[k8s.ProxyGIDAnnotation]; ok {
		v, err := strconv.ParseInt(override, 10, 64)
		if err == nil {
			values.Proxy.GID = v
		}
	}

	if override, ok := annotations[k8s.ProxyEnableExternalProfilesAnnotation]; ok {
		value, err := strconv.ParseBool(override)
		if err == nil {
			values.Proxy.EnableExternalProfiles = value
		}
	}

	if override, ok := annotations[k8s.ProxyInitImageAnnotation]; ok {
		values.ProxyInit.Image.Name = override
	}

	if override, ok := annotations[k8s.ProxyImagePullPolicyAnnotation]; ok {
		values.ProxyInit.Image.PullPolicy = override
	}

	if override, ok := annotations[k8s.ProxyIgnoreInboundPortsAnnotation]; ok {
		values.ProxyInit.IgnoreInboundPorts = override
	}

	if override, ok := annotations[k8s.ProxyIgnoreOutboundPortsAnnotation]; ok {
		values.ProxyInit.IgnoreOutboundPorts = override
	}

	if override, ok := annotations[k8s.ProxyOpaquePortsAnnotation]; ok {
		var opaquePorts strings.Builder
		for _, pr := range util.ParseContainerOpaquePorts(override, namedPorts) {
			if opaquePorts.Len() > 0 {
				opaquePorts.WriteRune(',')
			}
			opaquePorts.WriteString(pr.ToString())
		}

		values.Proxy.OpaquePorts = opaquePorts.String()
	}

	if override, ok := annotations[k8s.DebugImageAnnotation]; ok {
		values.DebugContainer.Image.Name = override
	}

	if override, ok := annotations[k8s.DebugImageVersionAnnotation]; ok {
		values.DebugContainer.Image.Version = override
	}

	if override, ok := annotations[k8s.DebugImagePullPolicyAnnotation]; ok {
		values.DebugContainer.Image.PullPolicy = override
	}

	if override, ok := annotations[k8s.ProxyAwait]; ok {
		if override == k8s.Enabled || override == k8s.Disabled {
			values.Proxy.Await = override == k8s.Enabled
		} else {
			log.Warnf("unrecognized value used for the %s annotation, valid values are: [%s, %s]", k8s.ProxyAwait, k8s.Enabled, k8s.Disabled)
		}
	}

	if override, ok := annotations[k8s.ProxyDefaultInboundPolicyAnnotation]; ok {
		if override != k8s.AllUnauthenticated && override != k8s.AllAuthenticated && override != k8s.ClusterUnauthenticated && override != k8s.ClusterAuthenticated && override != k8s.Deny && override != k8s.Audit {
			log.Warnf("unrecognized value used for the %s annotation, valid values are: [%s, %s, %s, %s, %s, %s]", k8s.ProxyDefaultInboundPolicyAnnotation, k8s.AllUnauthenticated, k8s.AllAuthenticated, k8s.ClusterUnauthenticated, k8s.ClusterAuthenticated, k8s.Deny, k8s.Audit)
		} else {
			values.Proxy.DefaultInboundPolicy = override
		}
	}

	if override, ok := annotations[k8s.ProxySkipSubnetsAnnotation]; ok {
		values.ProxyInit.SkipSubnets = override
	}

	if override, ok := annotations[k8s.ProxyAccessLogAnnotation]; ok {
		values.Proxy.AccessLog = override
	}
}

// NewResourceConfig creates and initializes a ResourceConfig
func NewResourceConfig(values *l5dcharts.Values, origin Origin, ns string) *ResourceConfig {
	config := &ResourceConfig{
		namespace:     ns,
		nsAnnotations: make(map[string]string),
		values:        values,
		origin:        origin,
	}

	config.workload.Meta = &metav1.ObjectMeta{}
	config.pod.meta = &metav1.ObjectMeta{}

	config.pod.labels = map[string]string{k8s.ControllerNSLabel: ns}
	config.pod.annotations = map[string]string{}
	return config
}

// WithKind enriches ResourceConfig with the workload kind
func (conf *ResourceConfig) WithKind(kind string) *ResourceConfig {
	conf.workload.metaType = metav1.TypeMeta{Kind: kind}
	return conf
}

// WithNsAnnotations enriches ResourceConfig with the namespace annotations, that can
// be used in shouldInject()
func (conf *ResourceConfig) WithNsAnnotations(m map[string]string) *ResourceConfig {
	conf.nsAnnotations = m
	return conf
}

// WithOwnerRetriever enriches ResourceConfig with a function that allows to retrieve
// the kind and name of the workload's owner reference
func (conf *ResourceConfig) WithOwnerRetriever(f OwnerRetrieverFunc) *ResourceConfig {
	conf.ownerRetriever = f
	return conf
}

// GetOwnerRef returns a reference to the resource's owner resource, if any
func (conf *ResourceConfig) GetOwnerRef() *metav1.OwnerReference {
	return conf.workload.ownerRef
}

func (conf *ResourceConfig) GetOverrideAnnotations() map[string]string {
	return conf.pod.annotations
}

func (conf *ResourceConfig) GetNsAnnotations() map[string]string {
	return conf.nsAnnotations
}

func (conf *ResourceConfig) GetWorkloadAnnotations() map[string]string {
	if conf.IsPod() {
		return conf.pod.meta.Annotations
	}

	return conf.workload.Meta.Annotations
}

// AppendPodAnnotations appends the given annotations to the pod spec in conf
func (conf *ResourceConfig) AppendPodAnnotations(annotations map[string]string) {
	for annotation, value := range annotations {
		conf.pod.annotations[annotation] = value
	}
}

// AppendPodAnnotation appends the given single annotation to the pod spec in conf
func (conf *ResourceConfig) AppendPodAnnotation(k, v string) {
	conf.pod.annotations[k] = v
}

// YamlMarshalObj returns the yaml for the workload in conf
func (conf *ResourceConfig) YamlMarshalObj() ([]byte, error) {
	j, err := getFilteredJSON(conf.workload.obj)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(j)
}

// ParseMetaAndYAML extracts the workload metadata and pod specs from the given
// input bytes. The results are stored in the conf's fields.
func (conf *ResourceConfig) ParseMetaAndYAML(bytes []byte) (*Report, error) {
	if err := conf.parse(bytes); err != nil {
		return nil, err
	}

	return newReport(conf), nil
}

// FromObject extracts the workload metadata and pod specs from the given
// runtime.Object instance. The results are stored in the conf's fields.
func (conf *ResourceConfig) FromObject(v runtime.Object) (*Report, error) {
	if err := conf.populateMeta(v); err != nil {
		return nil, err
	}

	return newReport(conf), nil
}

// GetValues returns the values used for rendering patches.
func (conf *ResourceConfig) GetValues() *l5dcharts.Values {
	return conf.values
}

func (conf *ResourceConfig) GetNodeSelector() map[string]string {
	if conf.HasPodTemplate() {
		return conf.pod.spec.NodeSelector
	}

	return nil
}

func (conf *ResourceConfig) GetAnnotationOverrides() map[string]string {
	overrides := map[string]string{}
	for k, v := range conf.pod.meta.Annotations {
		overrides[k] = v
	}

	if conf.origin != OriginCLI {
		for k, v := range conf.pod.annotations {
			overrides[k] = v
		}
	}
	return overrides
}

// GetPodPatch returns the JSON patch containing the proxy and init containers specs, if any.
// If injectProxy is false, only the config.linkerd.io annotations are set.
func GetPodPatch(conf *ResourceConfig, injectProxy bool, values *OverriddenValues, patchPathPrefix string) ([]JSONPatch, error) {
	patch := &podPatch{
		Values:      *values.Values,
		Annotations: map[string]string{},
		Labels:      map[string]string{},
		PathPrefix:  patchPathPrefix,
	}

	if conf.pod.spec != nil {
		conf.injectPodAnnotations(patch)
		if injectProxy {
			conf.injectObjectMeta(patch)
			conf.injectPodSpec(patch)
		} else {
			patch.Proxy = nil
			patch.ProxyInit = nil
		}
	}

	rawValues, err := yaml.Marshal(patch)
	if err != nil {
		return nil, err
	}

	files := []*loader.BufferedFile{
		{Name: chartutil.ChartfileName},
		{Name: "requirements.yaml"},
		{Name: "templates/patch.json"},
	}

	chart := &chartspkg.Chart{
		Name:      "patch",
		Dir:       "patch",
		Namespace: conf.namespace,
		RawValues: rawValues,
		Files:     files,
		Fs:        charts.Templates,
	}
	buf, err := chart.Render()
	if err != nil {
		return nil, err
	}

	// Get rid of invalid trailing commas
	res := rTrail.ReplaceAll(buf.Bytes(), []byte("}\n]"))

	patchResult := []JSONPatch{}
	if err := json.Unmarshal(res, &patchResult); err != nil {
		return nil, err
	}

	return patchResult, nil
}

// GetConfigAnnotation returns two values. The first value is the annotation
// value for a given key. The second is used to decide whether or not the caller
// should add the annotation. The caller should not add the annotation if the
// resource already has its own.
func GetConfigOverride(annotationKey string, workloadAnn map[string]string, nsAnn map[string]string) (string, bool) {
	_, ok := workloadAnn[annotationKey]
	if ok {
		log.Debugf("using workload %s annotation value", annotationKey)
		return "", false
	}

	annotation, ok := nsAnn[annotationKey]
	if ok {
		log.Debugf("using namespace %s annotation value", annotationKey)
		return annotation, true
	}
	return "", false
}

// CreateOpaquePortsPatch creates a patch that will add the default
// list of opaque ports.
func (conf *ResourceConfig) CreateOpaquePortsPatch() ([]byte, error) {
	if conf.HasWorkloadAnnotation(k8s.ProxyOpaquePortsAnnotation) {
		// The workload already has the opaque ports annotation so a patch
		// does not need to be created.
		return nil, nil
	}
	workloadAnn := conf.workload.Meta.Annotations
	if conf.IsPod() {
		workloadAnn = conf.pod.meta.Annotations
	}

	opaquePorts, ok := GetConfigOverride(k8s.ProxyOpaquePortsAnnotation, workloadAnn, conf.nsAnnotations)
	if ok {
		// The workload's namespace has the opaque ports annotation, so it
		// should inherit that value. A patch is created which adds that
		// list.
		return conf.CreateAnnotationPatch(opaquePorts)
	}

	// Both the workload and the namespace do not have the annotation so a
	// patch is created which adds the default list.
	defaultPorts := strings.Split(conf.GetValues().Proxy.OpaquePorts, ",")
	var filteredPorts []string
	if conf.IsPod() {
		// The workload is a pod so only add the default opaque ports that it
		// exposes as container ports.
		filteredPorts = conf.FilterPodOpaquePorts(defaultPorts)
	} else if conf.IsService() {
		// The workload is a service so only add the default opaque ports that
		// are exposed as a service port, or targeted as a targetPort.
		service := conf.workload.obj.(*corev1.Service)
		for _, p := range service.Spec.Ports {
			port := strconv.Itoa(int(p.Port))
			if p.TargetPort.Type == 0 && p.TargetPort.IntVal == 0 {
				// The port's targetPort is not set, so add the port if is
				// opaque by default. Checking that targetPort is not set
				// avoids marking a port as opaque if it targets a port that
				// not opaque (e.g. port=3306 and targetPort=80; 3306 should
				// not be opaque)
				if util.ContainsString(port, defaultPorts) {
					filteredPorts = append(filteredPorts, port)
				}
			} else if util.ContainsString(strconv.Itoa(int(p.TargetPort.IntVal)), defaultPorts) {
				// The port's targetPort is set; if it is opaque then port
				// should also be opaque.
				filteredPorts = append(filteredPorts, port)
			}
		}
	}
	if len(filteredPorts) == 0 {
		// There are no default opaque ports to add so a patch does not need
		// to be created.
		return nil, nil
	}
	ports := strings.Join(filteredPorts, ",")
	return conf.CreateAnnotationPatch(ports)
}

// FilterPodOpaquePorts returns a list of opaque ports that a pod exposes that
// are also in the given default opaque ports list.
func (conf *ResourceConfig) FilterPodOpaquePorts(defaultPorts []string) []string {
	var filteredPorts []string
	for _, c := range conf.pod.spec.Containers {
		for _, p := range c.Ports {
			port := strconv.Itoa(int(p.ContainerPort))
			if util.ContainsString(port, defaultPorts) {
				filteredPorts = append(filteredPorts, port)
			}
		}
	}
	return filteredPorts
}

// HasWorkloadAnnotation returns true if the workload has the annotation set
// by the resource config or its metadata.
func (conf *ResourceConfig) HasWorkloadAnnotation(annotation string) bool {
	if _, ok := conf.pod.meta.Annotations[annotation]; ok {
		return true
	}
	if _, ok := conf.workload.Meta.Annotations[annotation]; ok {
		return true
	}
	_, ok := conf.pod.annotations[annotation]
	return ok
}

// CreateAnnotationPatch returns a json patch which adds the opaque ports
// annotation with the `opaquePorts` value.
func (conf *ResourceConfig) CreateAnnotationPatch(opaquePorts string) ([]byte, error) {
	addRootAnnotations := false
	if conf.IsPod() {
		addRootAnnotations = len(conf.pod.meta.Annotations) == 0
	} else {
		addRootAnnotations = len(conf.workload.Meta.Annotations) == 0
	}

	patch := &annotationPatch{
		AddRootAnnotations: addRootAnnotations,
		OpaquePorts:        opaquePorts,
	}
	t, err := template.New("tpl").Parse(tpl)
	if err != nil {
		return nil, err
	}
	var patchJSON bytes.Buffer
	if err = t.Execute(&patchJSON, patch); err != nil {
		return nil, err
	}
	return patchJSON.Bytes(), nil
}

// Note this switch also defines what kinds are injectable
func (conf *ResourceConfig) getFreshWorkloadObj() runtime.Object {
	switch strings.ToLower(conf.workload.metaType.Kind) {
	case k8s.Deployment:
		return &appsv1.Deployment{}
	case k8s.ReplicationController:
		return &corev1.ReplicationController{}
	case k8s.ReplicaSet:
		return &appsv1.ReplicaSet{}
	case k8s.Job:
		return &batchv1.Job{}
	case k8s.DaemonSet:
		return &appsv1.DaemonSet{}
	case k8s.StatefulSet:
		return &appsv1.StatefulSet{}
	case k8s.Pod:
		return &corev1.Pod{}
	case k8s.Namespace:
		return &corev1.Namespace{}
	case k8s.CronJob:
		return &batchv1.CronJob{}
	case k8s.Service:
		return &corev1.Service{}
	}

	return nil
}

// JSONToYAML is a replacement for the same function in sigs.k8s.io/yaml
// that does conserve the field order as portrayed in k8s' api structs
func (conf *ResourceConfig) JSONToYAML(bytes []byte) ([]byte, error) {
	obj := conf.getFreshWorkloadObj()
	if err := json.Unmarshal(bytes, obj); err != nil {
		return nil, err
	}

	j, err := getFilteredJSON(obj)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(j)
}

func (conf *ResourceConfig) populateMeta(obj runtime.Object) error {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyDeploymentLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.Template)

	case *corev1.ReplicationController:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyReplicationControllerLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(v.Spec.Template)

	case *appsv1.ReplicaSet:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyReplicaSetLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.Template)

	case *batchv1.Job:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyJobLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.Template)

	case *appsv1.DaemonSet:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyDaemonSetLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.Template)

	case *appsv1.StatefulSet:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyStatefulSetLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.Template)

	case *corev1.Namespace:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		if conf.workload.Meta.Annotations == nil {
			conf.workload.Meta.Annotations = map[string]string{}
		}

	case *batchv1.CronJob:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		conf.pod.labels[k8s.ProxyCronJobLabel] = v.Name
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		conf.complete(&v.Spec.JobTemplate.Spec.Template)

	case *corev1.Pod:
		conf.workload.obj = v
		conf.pod.spec = &v.Spec
		conf.pod.meta = &v.ObjectMeta

		if conf.ownerRetriever != nil {
			kind, name, err := conf.ownerRetriever(v)
			if err != nil {
				return err
			}
			conf.workload.ownerRef = &metav1.OwnerReference{Kind: kind, Name: name}
			switch kind {
			case k8s.Deployment:
				conf.pod.labels[k8s.ProxyDeploymentLabel] = name
			case k8s.ReplicationController:
				conf.pod.labels[k8s.ProxyReplicationControllerLabel] = name
			case k8s.ReplicaSet:
				conf.pod.labels[k8s.ProxyReplicaSetLabel] = name
			case k8s.Job:
				conf.pod.labels[k8s.ProxyJobLabel] = name
			case k8s.DaemonSet:
				conf.pod.labels[k8s.ProxyDaemonSetLabel] = name
			case k8s.StatefulSet:
				conf.pod.labels[k8s.ProxyStatefulSetLabel] = name
			}
		}
		conf.pod.labels[k8s.WorkloadNamespaceLabel] = v.Namespace
		if conf.pod.meta.Annotations == nil {
			conf.pod.meta.Annotations = map[string]string{}
		}

	case *corev1.Service:
		conf.workload.obj = v
		conf.workload.Meta = &v.ObjectMeta
		if conf.workload.Meta.Annotations == nil {
			conf.workload.Meta.Annotations = map[string]string{}
		}

	default:
		return fmt.Errorf("unsupported type %T", v)
	}

	return nil
}

// parse parses the bytes payload, filling the gaps in ResourceConfig
// depending on the workload kind
func (conf *ResourceConfig) parse(bytes []byte) error {
	// The Kubernetes API is versioned and each version has an API modeled
	// with its own distinct Go types. If we tell `yaml.Unmarshal()` which
	// version we support then it will provide a representation of that
	// object using the given type if possible. However, it only allows us
	// to supply one object (of one type), so first we have to determine
	// what kind of object `bytes` represents so we can pass an object of
	// the correct type to `yaml.Unmarshal()`.
	// ---------------------------------------
	// Note: bytes is expected to be YAML and will only modify it when a
	// supported type is found. Otherwise, conf is left unmodified.

	// When injecting the linkerd proxy into a linkerd controller pod. The linkerd proxy's
	// LINKERD2_PROXY_DESTINATION_SVC_ADDR variable must be set to localhost for
	// the following reasons:
	//	1. According to https://github.com/kubernetes/minikube/issues/1568, minikube has an issue
	//     where pods are unable to connect to themselves through their associated service IP.
	//     Setting the LINKERD2_PROXY_DESTINATION_SVC_ADDR to localhost allows the
	//     proxy to bypass kube DNS name resolution as a workaround to this issue.
	//  2. We avoid the TLS overhead in encrypting and decrypting intra-pod traffic i.e. traffic
	//     between containers in the same pod.
	//  3. Using a Service IP instead of localhost would mean intra-pod traffic would be load-balanced
	//     across all controller pod replicas. This is undesirable as we would want all traffic between
	//	   containers to be self contained.
	//  4. We skip recording telemetry for intra-pod traffic within the control plane.

	if err := yaml.Unmarshal(bytes, &conf.workload.metaType); err != nil {
		return err
	}
	obj := conf.getFreshWorkloadObj()

	switch v := obj.(type) {
	case *appsv1.Deployment,
		*corev1.ReplicationController,
		*appsv1.ReplicaSet,
		*batchv1.Job,
		*appsv1.DaemonSet,
		*appsv1.StatefulSet,
		*corev1.Namespace,
		*batchv1.CronJob,
		*corev1.Pod,
		*corev1.Service:
		if err := yaml.Unmarshal(bytes, v); err != nil {
			return err
		}

		if err := conf.populateMeta(v); err != nil {
			return err
		}

	default:
		// unmarshal the metadata of other resource kinds like namespace, secret,
		// config map etc. to be used in the report struct
		if err := yaml.Unmarshal(bytes, &conf.workload); err != nil {
			return err
		}
	}

	return nil
}

func (conf *ResourceConfig) complete(template *corev1.PodTemplateSpec) {
	conf.pod.spec = &template.Spec
	conf.pod.meta = &template.ObjectMeta
	if conf.pod.meta.Annotations == nil {
		conf.pod.meta.Annotations = map[string]string{}
	}
}

// injectPodSpec adds linkerd sidecars to the provided PodSpec.
func (conf *ResourceConfig) injectPodSpec(values *podPatch) {
	saVolumeMount := conf.serviceAccountVolumeMount()

	// use the primary container's capabilities to ensure psp compliance, if
	// enabled
	if len(conf.pod.spec.Containers) > 0 {
		if sc := conf.pod.spec.Containers[0].SecurityContext; sc != nil && sc.Capabilities != nil {
			values.Proxy.Capabilities = &l5dcharts.Capabilities{
				Add:  []string{},
				Drop: []string{},
			}
			for _, add := range sc.Capabilities.Add {
				values.Proxy.Capabilities.Add = append(values.Proxy.Capabilities.Add, string(add))
			}
			for _, drop := range sc.Capabilities.Drop {
				values.Proxy.Capabilities.Drop = append(values.Proxy.Capabilities.Drop, string(drop))
			}
		}
	}

	if saVolumeMount != nil {
		values.Proxy.SAMountPath = &l5dcharts.VolumeMountPath{
			Name:      saVolumeMount.Name,
			MountPath: saVolumeMount.MountPath,
			ReadOnly:  saVolumeMount.ReadOnly,
		}
	}

	if v := conf.pod.meta.Annotations[k8s.ProxyEnableDebugAnnotation]; v != "" {
		debug, err := strconv.ParseBool(v)
		if err != nil {
			log.Warnf("unrecognized value used for the %s annotation: %s", k8s.ProxyEnableDebugAnnotation, v)
			debug = false
		}

		if debug {
			log.Infof("inject debug container")
			values.DebugContainer = &l5dcharts.DebugContainer{
				Image: &l5dcharts.Image{
					Name:       values.Values.DebugContainer.Image.Name,
					Version:    values.Values.DebugContainer.Image.Version,
					PullPolicy: values.Values.DebugContainer.Image.PullPolicy,
				},
			}
		}
	}

	conf.injectProxyInit(values)
	values.AddRootVolumes = len(conf.pod.spec.Volumes) == 0
}

func (conf *ResourceConfig) injectProxyInit(values *podPatch) {

	// Fill common fields from Proxy into ProxyInit
	if values.Proxy.Capabilities != nil {
		values.ProxyInit.Capabilities = &l5dcharts.Capabilities{}
		values.ProxyInit.Capabilities.Add = values.Proxy.Capabilities.Add
		values.ProxyInit.Capabilities.Drop = []string{}
		for _, drop := range values.Proxy.Capabilities.Drop {
			// Skip NET_RAW and NET_ADMIN as the init container requires them to setup iptables.
			if drop == "NET_RAW" || drop == "NET_ADMIN" {
				continue
			}
			values.ProxyInit.Capabilities.Drop = append(values.ProxyInit.Capabilities.Drop, drop)
		}
	}

	values.ProxyInit.SAMountPath = values.Proxy.SAMountPath

	if v := conf.pod.meta.Annotations[k8s.CloseWaitTimeoutAnnotation]; v != "" {
		closeWait, err := time.ParseDuration(v)
		if err != nil {
			log.Warnf("invalid duration value used for the %s annotation: %s", k8s.CloseWaitTimeoutAnnotation, v)
		} else {
			values.ProxyInit.CloseWaitTimeoutSecs = int64(closeWait.Seconds())
		}
	}

	values.AddRootInitContainers = len(conf.pod.spec.InitContainers) == 0

}

func (conf *ResourceConfig) serviceAccountVolumeMount() *corev1.VolumeMount {
	// Probably always true, but want to be super-safe
	if containers := conf.pod.spec.Containers; len(containers) > 0 {
		for _, vm := range containers[0].VolumeMounts {
			if vm.MountPath == k8s.MountPathServiceAccount {
				vm := vm // pin
				return &vm
			}
		}
	}
	return nil
}

// Given a ObjectMeta, update ObjectMeta in place with the new labels and
// annotations.
func (conf *ResourceConfig) injectObjectMeta(values *podPatch) {

	// Default proxy version to linkerd version
	if values.Proxy.Image.Version != "" {
		values.Annotations[k8s.ProxyVersionAnnotation] = values.Proxy.Image.Version
	} else {
		values.Annotations[k8s.ProxyVersionAnnotation] = values.LinkerdVersion
	}

	// Add the cert bundle's checksum to the workload's annotations.
	checksumBytes := sha256.Sum256([]byte(values.IdentityTrustAnchorsPEM))
	checksum := hex.EncodeToString(checksumBytes[:])
	values.Annotations[k8s.ProxyTrustRootSHA] = checksum

	if len(conf.pod.labels) > 0 {
		values.AddRootLabels = len(conf.pod.meta.Labels) == 0
		for _, k := range sortedKeys(conf.pod.labels) {
			values.Labels[k] = conf.pod.labels[k]
		}
	}
}

func (conf *ResourceConfig) injectPodAnnotations(values *podPatch) {
	// ObjectMetaAnnotations.Annotations is nil for new empty structs, but we always initialize
	// it to an empty map in parse() above, so we follow suit here.
	emptyMeta := &metav1.ObjectMeta{Annotations: map[string]string{}}
	// Cronjobs might have an empty `spec.jobTemplate.spec.template.metadata`
	// field so we make sure to create it if needed, before attempting adding annotations
	values.AddRootMetadata = reflect.DeepEqual(conf.pod.meta, emptyMeta)
	values.AddRootAnnotations = len(conf.pod.meta.Annotations) == 0

	for _, k := range sortedKeys(conf.pod.annotations) {
		values.Annotations[k] = conf.pod.annotations[k]

		// append any additional pod annotations to the pod's meta.
		// for e.g., annotations that were converted from CLI inject options.
		conf.pod.meta.Annotations[k] = conf.pod.annotations[k]
	}
}

// GetOverriddenConfiguration returns a map of the overridden proxy annotations
func (conf *ResourceConfig) GetOverriddenConfiguration() map[string]string {
	proxyOverrideConfig := map[string]string{}
	for _, annotation := range ProxyAnnotations {
		proxyOverrideConfig[annotation] = conf.pod.meta.Annotations[annotation]
	}

	return proxyOverrideConfig
}

// IsControlPlaneComponent returns true if the component is part of linkerd control plane
func (conf *ResourceConfig) IsControlPlaneComponent() bool {
	_, b := conf.pod.meta.Labels[k8s.ControllerComponentLabel]
	return b
}

func sortedKeys(m map[string]string) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// IsNamespace checks if a given config is a workload of Kind namespace
func (conf *ResourceConfig) IsNamespace() bool {
	return strings.ToLower(conf.workload.metaType.Kind) == k8s.Namespace
}

// IsService checks if a given config is a workload of Kind service
func (conf *ResourceConfig) IsService() bool {
	return strings.ToLower(conf.workload.metaType.Kind) == k8s.Service
}

// IsPod checks if a given config is a workload of Kind pod.
func (conf *ResourceConfig) IsPod() bool {
	return strings.ToLower(conf.workload.metaType.Kind) == k8s.Pod
}

// HasPodTemplate checks if a given config has a pod template spec.
func (conf *ResourceConfig) HasPodTemplate() bool {
	return conf.pod.meta != nil && conf.pod.spec != nil
}

// AnnotateNamespace annotates a namespace resource config with `annotations`.
func (conf *ResourceConfig) AnnotateNamespace(annotations map[string]string) ([]byte, error) {
	ns, ok := conf.workload.obj.(*corev1.Namespace)
	if !ok {
		return nil, errors.New("can't inject namespace. Type assertion failed")
	}
	ns.Annotations[k8s.ProxyInjectAnnotation] = k8s.ProxyInjectEnabled
	if len(annotations) > 0 {
		for annotation, value := range annotations {
			ns.Annotations[annotation] = value
		}
	}
	j, err := getFilteredJSON(ns)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(j)
}

// AnnotateService annotates a service resource config with `annotations`.
func (conf *ResourceConfig) AnnotateService(annotations map[string]string) ([]byte, error) {
	service, ok := conf.workload.obj.(*corev1.Service)
	if !ok {
		return nil, errors.New("can't inject service. Type assertion failed")
	}
	if len(annotations) > 0 {
		for annotation, value := range annotations {
			service.Annotations[annotation] = value
		}
	}
	j, err := getFilteredJSON(service)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(j)
}

// getFilteredJSON method performs JSON marshaling such that zero values of
// empty structs are respected by `omitempty` tags. We make use of a drop-in
// replacement of the standard json/encoding library, without which empty struct values
// present in workload objects would make it into the marshaled JSON.
func getFilteredJSON(conf runtime.Object) ([]byte, error) {
	return jsonfilter.Marshal(&conf)
}

// ToWholeCPUCores coerces a k8s resource value to a whole integer value, rounding up.
func ToWholeCPUCores(q k8sResource.Quantity) (int64, error) {
	q.RoundUp(0)
	if n, ok := q.AsInt64(); ok {
		return n, nil
	}
	return 0, fmt.Errorf("Could not parse cores: %s", q.String())
}

// getPodInboundPorts will return a string-formatted list of ports (in ascending
// order) based on a PodSpec object. The function will check each container in
// the pod and extract any defined ports. Additionally, it will also extract any
// healthcheck target probes, provided the probe is an HTTP healthcheck
func getPodInboundPorts(podSpec *corev1.PodSpec) string {
	ports := make(map[int32]struct{})
	if podSpec != nil {
		for _, container := range podSpec.Containers {
			for _, port := range container.Ports {
				ports[port.ContainerPort] = struct{}{}
			}

			if readiness := container.ReadinessProbe; readiness != nil {
				if port, ok := getProbePort(readiness); ok {
					ports[port] = struct{}{}
				}
			}

			if liveness := container.LivenessProbe; liveness != nil {
				if port, ok := getProbePort(liveness); ok {
					ports[port] = struct{}{}
				}
			}
		}
	}

	portList := make([]string, 0, len(ports))
	for port := range ports {
		portList = append(portList, strconv.Itoa(int(port)))
	}

	// sort slice in ascending order
	sort.Strings(portList)
	return strings.Join(portList, ",")
}

// getProbePort takes the healthcheck probe spec of a container and returns the
// target port if the probe is configured to do an HTTPGet. The function returns
// the probe's target port and a success value (if successful)
func getProbePort(probe *corev1.Probe) (int32, bool) {
	if probe.HTTPGet != nil {
		// HTTPGet probes use a named port, in this case, do not return it. A
		// named port must be declared in the container's own ports; if probe uses
		// a named port it is likely the port has been seen before.
		switch probe.HTTPGet.Port.Type {
		case intstr.Int:
			return probe.HTTPGet.Port.IntVal, true
		}
	}

	return 0, false
}
