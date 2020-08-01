package factory

import (
	"context"
	"fmt"
	"path"

	victoriametricsv1beta1 "github.com/VictoriaMetrics/operator/api/v1beta1"
	"github.com/VictoriaMetrics/operator/conf"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	SecretsDir      = "/etc/vm/secrets"
	ConfigMapsDir   = "/etc/vm/configs"
	vmSingleDataDir = "/victoria-metrics-data"
)

func CreateVMStorage(ctx context.Context, cr *victoriametricsv1beta1.VMSingle, rclient client.Client, c *conf.BaseOperatorConf) (*corev1.PersistentVolumeClaim, error) {

	l := log.WithValues("vm.single.pvc.create", cr.Name)
	l.Info("reconciling pvc")
	newPvc := makeVMSinglePvc(cr, c)
	existPvc := &corev1.PersistentVolumeClaim{}
	err := rclient.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.PrefixedName()}, existPvc)
	if err != nil {
		if errors.IsNotFound(err) {
			l.Info("creating new pvc for vmsingle")
			if err := rclient.Create(ctx, newPvc); err != nil {
				return nil, fmt.Errorf("cannot create new pvc for vmsingle: %w", err)
			}

			return newPvc, nil
		} else {
			return nil, fmt.Errorf("cannot get existing pvc for vmsingle: %w", err)
		}
	}

	if existPvc.Spec.Resources.String() != newPvc.Spec.Resources.String() {
		l.Info("volume requests isn't same, update required")
		existPvc.Spec.Resources = newPvc.Spec.Resources
		err := rclient.Update(ctx, existPvc)
		if err != nil {
			l.Error(err, "cannot update pvc size, we can suppress it")
		}
	}
	newPvc = existPvc

	return newPvc, nil
}

func makeVMSinglePvc(cr *victoriametricsv1beta1.VMSingle, c *conf.BaseOperatorConf) *corev1.PersistentVolumeClaim {
	pvcObject := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cr.PrefixedName(),
			Namespace:   cr.Namespace,
			Labels:      c.Labels.Merge(cr.FinalLabels()),
			Annotations: cr.Annotations(),
		},
		Spec: *cr.Spec.Storage,
	}
	if cr.Spec.RemovePvcAfterDelete {
		pvcObject.OwnerReferences = cr.AsOwner()
	}
	return pvcObject
}

func CreateOrUpdateVMSingle(ctx context.Context, cr *victoriametricsv1beta1.VMSingle, rclient client.Client, c *conf.BaseOperatorConf) (*appsv1.Deployment, error) {

	l := log.WithValues("controller", "vmsingle.crud", "vmsingle", cr.Name)
	l.Info("create or update vm single deploy")

	newDeploy, err := newDeployForVMSingle(cr, c)
	if err != nil {
		return nil, fmt.Errorf("cannot generate new deploy for vmsingle: %w", err)
	}

	l = l.WithValues("single.deploy.name", newDeploy.Name, "single.deploy.namespace", newDeploy.Namespace)

	currentDeploy := &appsv1.Deployment{}
	err = rclient.Get(ctx, types.NamespacedName{Name: newDeploy.Name, Namespace: newDeploy.Namespace}, currentDeploy)
	if err != nil {
		if errors.IsNotFound(err) {
			//create new
			l.Info("vmsingle deploy not found, creating new one")
			err := rclient.Create(ctx, newDeploy)
			if err != nil {
				return nil, fmt.Errorf("cannot create new vmsingle deploy: %w", err)
			}
			l.Info("new vmsingle deploy was created")
		} else {
			return nil, fmt.Errorf("cannot get vmsingle deploy: %w", err)
		}
	}
	l.Info("vm vmsingle was found, updating it")
	for annotation, value := range currentDeploy.Annotations {
		newDeploy.Annotations[annotation] = value
	}

	for annotation, value := range currentDeploy.Spec.Template.Annotations {
		newDeploy.Spec.Template.Annotations[annotation] = value
	}

	err = rclient.Update(ctx, newDeploy)
	if err != nil {
		return nil, fmt.Errorf("cannot upddate vmsingle deploy: %w", err)
	}
	l.Info("single deploy reconciled")

	return newDeploy, nil
}

func newDeployForVMSingle(cr *victoriametricsv1beta1.VMSingle, c *conf.BaseOperatorConf) (*appsv1.Deployment, error) {
	cr = cr.DeepCopy()

	if cr.Spec.Image == nil {
		cr.Spec.Image = &c.VMSingleDefault.Image
	}
	if cr.Spec.Version == "" {
		cr.Spec.Version = c.VMSingleDefault.Version
	}
	if cr.Spec.Port == "" {
		cr.Spec.Port = c.VMSingleDefault.Port
	}

	if cr.Spec.Resources.Requests == nil {
		cr.Spec.Resources.Requests = corev1.ResourceList{}
	}
	if cr.Spec.Resources.Limits == nil {
		cr.Spec.Resources.Limits = corev1.ResourceList{}
	}
	if _, ok := cr.Spec.Resources.Limits[corev1.ResourceMemory]; !ok {
		cr.Spec.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(c.VMSingleDefault.Resource.Limit.Mem)
	}
	if _, ok := cr.Spec.Resources.Limits[corev1.ResourceCPU]; !ok {
		cr.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse(c.VMSingleDefault.Resource.Limit.Cpu)
	}

	if _, ok := cr.Spec.Resources.Requests[corev1.ResourceMemory]; !ok {
		cr.Spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(c.VMSingleDefault.Resource.Request.Mem)
	}
	if _, ok := cr.Spec.Resources.Requests[corev1.ResourceCPU]; !ok {
		cr.Spec.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(c.VMSingleDefault.Resource.Request.Cpu)
	}

	podSpec, err := makeSpecForVMSingle(cr, c)
	if err != nil {
		return nil, err
	}

	depSpec := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cr.PrefixedName(),
			Namespace:       cr.Namespace,
			Labels:          c.Labels.Merge(cr.FinalLabels()),
			Annotations:     cr.Annotations(),
			OwnerReferences: cr.AsOwner(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: cr.Spec.ReplicaCount,
			Selector: &metav1.LabelSelector{
				MatchLabels: cr.SelectorLabels(),
			},
			Strategy: appsv1.DeploymentStrategy{
				//we use recreate, coz of volume claim
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Template: *podSpec,
		},
	}

	return depSpec, nil
}

func makeSpecForVMSingle(cr *victoriametricsv1beta1.VMSingle, c *conf.BaseOperatorConf) (*corev1.PodTemplateSpec, error) {
	args := []string{
		fmt.Sprintf("-storageDataPath=%s", vmSingleDataDir),
		fmt.Sprintf("-retentionPeriod=%s", cr.Spec.RetentionPeriod),
	}
	if cr.Spec.LogLevel != "" {
		args = append(args, fmt.Sprintf("-loggerLevel=%s", cr.Spec.LogLevel))
	}
	if cr.Spec.LogFormat != "" {
		args = append(args, fmt.Sprintf("-loggerFormat=%s", cr.Spec.LogFormat))
	}

	for arg, value := range cr.Spec.ExtraArgs {
		args = append(args, fmt.Sprintf("--%s=%s", arg, value))
	}

	args = append(args, fmt.Sprintf("-httpListenAddr=:%s", cr.Spec.Port))

	var envs []corev1.EnvVar
	envs = append(envs, cr.Spec.ExtraEnvs...)

	var ports []corev1.ContainerPort
	ports = append(ports, corev1.ContainerPort{Name: "http", Protocol: "TCP", ContainerPort: intstr.Parse(cr.Spec.Port).IntVal})
	volumes := []corev1.Volume{}

	storageSpec := cr.Spec.Storage
	if storageSpec == nil {
		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	} else {

		volumes = append(volumes, corev1.Volume{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: cr.PrefixedName(),
				},
			},
		})
	}
	volumes = append(volumes, cr.Spec.Volumes...)
	vmMounts := []corev1.VolumeMount{
		{
			Name:      "data",
			MountPath: vmSingleDataDir,
		},
	}

	vmMounts = append(vmMounts, cr.Spec.VolumeMounts...)

	for _, s := range cr.Spec.Secrets {
		volumes = append(volumes, corev1.Volume{
			Name: SanitizeVolumeName("secret-" + s),
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: s,
				},
			},
		})
		vmMounts = append(vmMounts, corev1.VolumeMount{
			Name:      SanitizeVolumeName("secret-" + s),
			ReadOnly:  true,
			MountPath: path.Join(SecretsDir, s),
		})
	}

	for _, c := range cr.Spec.ConfigMaps {
		volumes = append(volumes, corev1.Volume{
			Name: SanitizeVolumeName("configmap-" + c),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: c,
					},
				},
			},
		})
		vmMounts = append(vmMounts, corev1.VolumeMount{
			Name:      SanitizeVolumeName("configmap-" + c),
			ReadOnly:  true,
			MountPath: path.Join(ConfigMapsDir, c),
		})
	}

	livenessProbeHandler := corev1.Handler{
		HTTPGet: &corev1.HTTPGetAction{
			Port:   intstr.Parse(cr.Spec.Port),
			Scheme: "HTTP",
			Path:   "/health",
		},
	}
	readinessProbeHandler := corev1.Handler{
		HTTPGet: &corev1.HTTPGetAction{
			Port:   intstr.Parse(cr.Spec.Port),
			Scheme: "HTTP",
			Path:   "/health",
		},
	}
	livenessFailureThreshold := int32(3)
	livenessProbe := &corev1.Probe{
		Handler:          livenessProbeHandler,
		PeriodSeconds:    5,
		TimeoutSeconds:   probeTimeoutSeconds,
		FailureThreshold: livenessFailureThreshold,
	}
	readinessProbe := &corev1.Probe{
		Handler:          readinessProbeHandler,
		TimeoutSeconds:   probeTimeoutSeconds,
		PeriodSeconds:    5,
		FailureThreshold: 10,
	}

	var additionalContainers []corev1.Container

	operatorContainers := append([]corev1.Container{
		{
			Name:                     "vmsignle",
			Image:                    fmt.Sprintf("%s:%s", *cr.Spec.Image, cr.Spec.Version),
			Ports:                    ports,
			Args:                     args,
			VolumeMounts:             vmMounts,
			LivenessProbe:            livenessProbe,
			ReadinessProbe:           readinessProbe,
			Resources:                cr.Spec.Resources,
			Env:                      envs,
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		},
	}, additionalContainers...)

	containers, err := MergePatchContainers(operatorContainers, cr.Spec.Containers)
	if err != nil {
		return nil, err
	}

	vmSignleSpec := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      cr.PodLabels(),
			Annotations: cr.PodAnnotations(),
		},
		Spec: corev1.PodSpec{
			Volumes:            volumes,
			InitContainers:     cr.Spec.InitContainers,
			Containers:         containers,
			ServiceAccountName: cr.Spec.ServiceAccountName,
			SecurityContext:    cr.Spec.SecurityContext,
			ImagePullSecrets:   cr.Spec.ImagePullSecrets,
			Affinity:           cr.Spec.Affinity,
			SchedulerName:      "",
			Tolerations:        cr.Spec.Tolerations,
			PriorityClassName:  cr.Spec.PriorityClassName,
			HostNetwork:        cr.Spec.HostNetwork,
			DNSPolicy:          cr.Spec.DNSPolicy,
		},
	}

	return vmSignleSpec, nil

}

func CreateOrUpdateVMSingleService(ctx context.Context, cr *victoriametricsv1beta1.VMSingle, rclient client.Client, c *conf.BaseOperatorConf) (*corev1.Service, error) {
	l := log.WithValues("controller", "vmalert.service.crud")
	newService := newServiceVMSingle(cr, c)

	currentService := &corev1.Service{}
	err := rclient.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: newService.Name}, currentService)
	if err != nil {
		if errors.IsNotFound(err) {
			l.Info("creating new service for vm vmsingle")
			err := rclient.Create(ctx, newService)
			if err != nil {
				return nil, fmt.Errorf("cannot create new service for vmsingle")
			}
		} else {
			return nil, fmt.Errorf("cannot get vmsingle service: %w", err)
		}
	}
	for annotation, value := range currentService.Annotations {
		newService.Annotations[annotation] = value
	}
	if currentService.Spec.ClusterIP != "" {
		newService.Spec.ClusterIP = currentService.Spec.ClusterIP
	}
	if currentService.ResourceVersion != "" {
		newService.ResourceVersion = currentService.ResourceVersion
	}
	err = rclient.Update(ctx, newService)
	if err != nil {
		return nil, fmt.Errorf("cannot update vmsingle service: %w", err)
	}
	l.Info("vmsingle svc reconciled")
	return newService, nil
}

func newServiceVMSingle(cr *victoriametricsv1beta1.VMSingle, c *conf.BaseOperatorConf) *corev1.Service {
	cr = cr.DeepCopy()
	if cr.Spec.Port == "" {
		cr.Spec.Port = c.VMSingleDefault.Port
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cr.PrefixedName(),
			Namespace:       cr.Namespace,
			Labels:          c.Labels.Merge(cr.FinalLabels()),
			Annotations:     cr.Annotations(),
			OwnerReferences: cr.AsOwner(),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: cr.SelectorLabels(),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   "TCP",
					Port:       intstr.Parse(cr.Spec.Port).IntVal,
					TargetPort: intstr.Parse(cr.Spec.Port),
				},
			},
		},
	}
}