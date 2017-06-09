package controller

import (
	"fmt"
	"hash/crc32"

	"k8s.io/client-go/pkg/api/v1"
	extv1beta "k8s.io/client-go/pkg/apis/extensions/v1beta1"

	"github.com/zalando-incubator/postgres-operator/pkg/cluster"
	"github.com/zalando-incubator/postgres-operator/pkg/types"
	"github.com/zalando-incubator/postgres-operator/pkg/util/config"
	"github.com/zalando-incubator/postgres-operator/pkg/util/constants"
	"github.com/zalando-incubator/postgres-operator/pkg/util/k8sutil"
)

func (c *Controller) makeClusterConfig() cluster.Config {
	infrastructureRoles := make(map[string]types.PgUser)
	for k, v := range c.InfrastructureRoles {
		infrastructureRoles[k] = v
	}

	return cluster.Config{
		KubeClient:          c.KubeClient,
		RestClient:          c.RestClient,
		RestConfig:          c.RestConfig,
		TeamsAPIClient:      c.TeamsAPIClient,
		OpConfig:            config.Copy(c.opConfig),
		InfrastructureRoles: infrastructureRoles,
	}
}

func thirdPartyResource(TPRName string) *extv1beta.ThirdPartyResource {
	return &extv1beta.ThirdPartyResource{
		ObjectMeta: v1.ObjectMeta{
			//ThirdPartyResources are cluster-wide
			Name: TPRName,
		},
		Versions: []extv1beta.APIVersion{
			{Name: constants.TPRApiVersion},
		},
		Description: constants.TPRDescription,
	}
}

func (c *Controller) clusterWorkerID(clusterName types.NamespacedName) uint32 {
	return crc32.ChecksumIEEE([]byte(clusterName.String())) % c.opConfig.Workers
}

func (c *Controller) createTPR() error {
	TPRName := fmt.Sprintf("%s.%s", constants.TPRName, constants.TPRVendor)
	tpr := thirdPartyResource(TPRName)

	_, err := c.KubeClient.ExtensionsV1beta1().ThirdPartyResources().Create(tpr)
	if err != nil {
		if !k8sutil.ResourceAlreadyExists(err) {
			return err
		}
		c.logger.Infof("ThirdPartyResource '%s' is already registered", TPRName)
	} else {
		c.logger.Infof("ThirdPartyResource '%s' has been registered", TPRName)
	}

	return k8sutil.WaitTPRReady(c.RestClient, c.opConfig.TPR.ReadyWaitInterval, c.opConfig.TPR.ReadyWaitTimeout, c.opConfig.Namespace)
}

func (c *Controller) getInfrastructureRoles() (result map[string]types.PgUser, err error) {
	if c.opConfig.InfrastructureRolesSecretName == (types.NamespacedName{}) {
		// we don't have infrastructure roles defined, bail out
		return nil, nil
	}

	infraRolesSecret, err := c.KubeClient.
		Secrets(c.opConfig.InfrastructureRolesSecretName.Namespace).
		Get(c.opConfig.InfrastructureRolesSecretName.Name)
	if err != nil {
		c.logger.Debugf("Infrastructure roles secret name: %s", c.opConfig.InfrastructureRolesSecretName)
		return nil, fmt.Errorf("could not get infrastructure roles secret: %v", err)
	}

	data := infraRolesSecret.Data
	result = make(map[string]types.PgUser)
Users:
	// in worst case we would have one line per user
	for i := 1; i <= len(data); i++ {
		properties := []string{"user", "password", "inrole"}
		t := types.PgUser{}
		for _, p := range properties {
			key := fmt.Sprintf("%s%d", p, i)
			if val, present := data[key]; !present {
				if p == "user" {
					// exit when the user name with the next sequence id is absent
					break Users
				}
			} else {
				s := string(val)
				switch p {
				case "user":
					t.Name = s
				case "password":
					t.Password = s
				case "inrole":
					t.MemberOf = append(t.MemberOf, s)
				default:
					c.logger.Warnf("Unknown key %s", p)
				}
			}
		}

		if t.Name != "" {
			result[t.Name] = t
		}
	}

	return result, nil
}

func (c *Controller) podClusterName(pod *v1.Pod) types.NamespacedName {
	if name, ok := pod.Labels[c.opConfig.ClusterNameLabel]; ok {
		return types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      name,
		}
	}

	return types.NamespacedName{}
}
