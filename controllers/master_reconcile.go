/*
Copyright 2022 nobolity.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	dsv1alpha1 "dolphinscheduler-operator/api/v1alpha1"
	v2 "k8s.io/api/autoscaling/v2"
	_ "k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *DSMasterReconciler) podMemberSet(ctx context.Context, cluster *dsv1alpha1.DSMaster) (MemberSet, error) {
	members := MemberSet{}
	pods := &corev1.PodList{}

	if err := r.Client.List(ctx, pods, client.InNamespace(cluster.Namespace),
		client.MatchingLabels(LabelsForCluster(dsv1alpha1.DsMasterLabel))); err != nil {
		return members, err
	}

	if len(pods.Items) > 0 {
		for _, pod := range pods.Items {
			if pod.ObjectMeta.DeletionTimestamp.IsZero() {
				m := &Member{
					Name:            pod.Name,
					Namespace:       pod.Namespace,
					Created:         true,
					Version:         pod.Labels[dsv1alpha1.DsVersionLabel],
					Phase:           string(pod.Status.Phase),
					RunningAndReady: IsRunningAndReady(&pod),
				}
				members.Add(m)
			}
		}
	}

	return members, nil
}

func newDSMasterPod(cr *dsv1alpha1.DSMaster) *corev1.Pod {
	var isSetHostnameAsFQDN bool
	isSetHostnameAsFQDN = true
	var podName = cr.Name + "-pod" + dsv1alpha1.RandStr(6)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: cr.Namespace,
			Labels: map[string]string{dsv1alpha1.DsAppName: dsv1alpha1.DsMasterLabel,
				dsv1alpha1.DsVersionLabel: cr.Spec.Version,
				dsv1alpha1.DsServiceLabel: dsv1alpha1.DsServiceLabelValue},
		},
		Spec: corev1.PodSpec{
			Hostname:          podName,
			Subdomain:         dsv1alpha1.DsServiceLabelValue,
			SetHostnameAsFQDN: &isSetHostnameAsFQDN,

			Containers: []corev1.Container{
				{
					Name:            cr.Name,
					Image:           ImageName(cr.Spec.Repository, cr.Spec.Version),
					ImagePullPolicy: corev1.PullIfNotPresent,
					Env: []corev1.EnvVar{
						{
							Name:  dsv1alpha1.EnvZookeeper,
							Value: cr.Spec.ZookeeperConnect,
						}, {
							Name:  dsv1alpha1.DataSourceDriveName,
							Value: cr.Spec.Datasource.DriveName,
						},
						{
							Name:  dsv1alpha1.DataSourceUrl,
							Value: cr.Spec.Datasource.Url,
						},
						{
							Name:  dsv1alpha1.DataSourceUserName,
							Value: cr.Spec.Datasource.UserName,
						},
						{
							Name:  dsv1alpha1.DataSourcePassWord,
							Value: cr.Spec.Datasource.Password,
						},
					},
				},
			},
		},
	}
}

func (r *DSMasterReconciler) ensureDSMasterDeleted(ctx context.Context, DSMaster *dsv1alpha1.DSMaster) error {
	if err := r.Client.Delete(ctx, DSMaster, client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		return err
	}
	return nil
}

func (r *DSMasterReconciler) newDSMasterPod(cluster *dsv1alpha1.DSMaster) (*corev1.Pod, error) {
	// Create pod
	pod := newDSMasterPod(cluster)
	if err := controllerutil.SetControllerReference(cluster, pod, r.Scheme); err != nil {
		return nil, err
	}
	AddLogVolumeToPod(pod, cluster.Spec.LogPvcName)
	applyPodPolicy(pod, cluster.Spec.Pod)
	return pod, nil
}

func createMasterService(cluster *dsv1alpha1.DSMaster) *corev1.Service {
	labels_ := LabelsForService()
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dsv1alpha1.DsHeadLessServiceLabel,
			Namespace: cluster.Namespace,
			Labels:    map[string]string{dsv1alpha1.DsAppName: dsv1alpha1.DsHeadLessServiceLabel},
		},
		Spec: corev1.ServiceSpec{
			Selector:                 labels_,
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true,
		},
	}
	return &service
}

func (r *DSMasterReconciler) createHPA(cluster *dsv1alpha1.DSMaster) *v2.HorizontalPodAutoscaler {
	hpa := v2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:            dsv1alpha1.DsWorkerHpa,
			Namespace:       cluster.Namespace,
			ResourceVersion: dsv1alpha1.DSVersion,
		},
		Spec: v2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: v2.CrossVersionObjectReference{
				Kind:       dsv1alpha1.DsWorkerKind,
				Name:       dsv1alpha1.DsWorkerLabel,
				APIVersion: dsv1alpha1.APIVersion,
			},
			MinReplicas: &cluster.Spec.HpaPolicy.MinReplicas,
			MaxReplicas: cluster.Spec.HpaPolicy.MaxReplicas,
		},
	}

	if cluster.Spec.HpaPolicy.CPUAverageUtilization > 0 {
		hpa.Spec.Metrics = append(hpa.Spec.Metrics, v2.MetricSpec{
			Type: v2.ResourceMetricSourceType,
			Resource: &v2.ResourceMetricSource{
				Name: corev1.ResourceCPU,
				Target: v2.MetricTarget{
					Type:               v2.UtilizationMetricType,
					AverageUtilization: &cluster.Spec.HpaPolicy.CPUAverageUtilization,
				},
			},
		})
	}

	if cluster.Spec.HpaPolicy.MEMAverageUtilization > 0 {
		hpa.Spec.Metrics = append(hpa.Spec.Metrics, v2.MetricSpec{
			Type: v2.ResourceMetricSourceType,
			Resource: &v2.ResourceMetricSource{
				Name: corev1.ResourceMemory,
				Target: v2.MetricTarget{
					Type:               v2.UtilizationMetricType,
					AverageUtilization: &cluster.Spec.HpaPolicy.MEMAverageUtilization,
				},
			},
		})
	}

	return &hpa
}

func (r *DSMasterReconciler) deleteHPA(ctx context.Context, hpa *v2.HorizontalPodAutoscaler) error {
	if err := r.Client.Delete(ctx, hpa, client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		return err
	}
	return nil
}
