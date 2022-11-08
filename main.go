// Copyright 2022 Google LLC
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//      http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"

	//"regexp"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Required for GKE client-go - https://github.com/kubernetes/client-go/issues/242#issuecomment-314642965
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	// secret manager stuff
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

var (
	namespace = flag.String("namespace", "default", "Name of the namespace to look for secrets, default: default")
	exclude   = flag.String("exclude", "", "Name of secrets to exclude, comma delimited, default: ''")
	project   = flag.String("project", "", "Name of GCP project to migrate secrets, default: ''")
	delete    = flag.Bool("delete", false, "If set `--delete` or `--delete=True` the Google secrets are deleted. If the secrets exist, the program breaks - on purpose")
	debug     = flag.Bool("debug", false, "If set `--debug` or `--debug=True` more logging is enabled")
)

type Client struct {
	k8sClient *kubernetes.Clientset
	gsmClient *secretmanager.Client
}

// newClient initializes connections with clients and returns combined client struct
func newClient() *Client {
	// check for incluster config, if not found then use the out-of-cluster (local) kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig :=
			clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("failed to initialize Kubernetes API client: %s", err)
		}
	}

	// create the Kubernetes clientset (clients for multiple APIs)
	k8sClientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to load Kubernetes clientset: %s", err)
	}
	log.Println("✅ Kubernetes client configured")

	// create the Google gcloud client
	gsmClient, err := secretmanager.NewClient(context.TODO())
	if err != nil {
		log.Fatalf("failed to setup client: %v", err)
	}
	log.Println("✅ Google client configured")

	return &Client{
		k8sClient: k8sClientSet, // returned *kubernetes.Clientset dynamically if IN/OUT of cluster based on getK8sClient()
		gsmClient: gsmClient,    // returned *kubernetes.Clientset dynamically if IN/OUT of cluster based on getK8sClient()
	}
}

func main() {
	// check input flags and set variables accordingly
	flag.Parse()

	// use the default namespace if "" accidentally passed as --namespace=${UNSET VARIABLE}
	if *namespace == "" {
		*namespace = "default"
	}

	// exit if project arguement isn't set
	if *project == "" {
		log.Fatalf("❌ `--project=` is not defined in arguments")
	}

	// kick-off!
	log.Printf("📣 Starting migration script [namespace: '%s'] [project: '%s']:\n", *namespace, *project)

	// init the struct for client(s) re-use
	log.Println("Initializing the clients:")
	c := newClient()

	// list secrets from a Kubernetes namespace (set by --namespace="" or defaults to "default")
	log.Printf("🔍 Getting all secrets from [namespace: '%s']", *namespace)
	secretsAll, err := c.getKubernetesSecrets(*namespace)
	if err != nil {
		log.Printf("Issue interacting with Kubernetes in [namespace: '%s']", *namespace)
	}

	// Fail and exit if results are empty
	if len(secretsAll.Items) == 0 {
		log.Printf("Error: Issue aquiring list of Kubernetes secrets in [namespace: '%s']", *namespace)
		log.Fatalf("❌ No secrets found to copy, no action taken")
	}

	// log the filtering taking place
	log.Printf("🪚 Filtering secret list to skip secrets with the name 'default-token-*','ca.crt','token','namespace'")

	// create a "originalList" of secret names only
	originalList := []string{}

	// create a "safeList" of secret names only for GCP
	safeList := []string{}

	for index, secret := range secretsAll.Items {

		if *debug {
			fmt.Printf("Found [%d]: %s\n", index+1, secret.Name)
		}

		// SKIP IF:
		// secret.Name starts with a "default-token-*" prefix indicating it's the default service account
		if strings.HasPrefix(secret.Name, "default-token-") {
			if *debug {
				log.Printf("Skipped adding secret ['%s'] to list by default", secret.Name)
			}
			continue
		}

		// build the array
		originalList = append(originalList, secret.Name)
	}

	// list after skipping
	log.Printf("📋 List: %s\n", originalList)

	// further list parsing to remove excludes
	log.Printf("🪚 Removing `--exclude` items ['%s']", *exclude)
	// seperate the "--exclude" list and remove any found values from the orginalList
	for _, exludeName := range strings.Split(*exclude, ",") {
		// remove item if found in originalList and excludeList by comparison
		originalList = remove(originalList, exludeName)
	}

	// fail if we have no objects to migrate
	if len(originalList) == 0 {
		log.Fatalf("❌ No secrets found to copy, no action taken")
	}

	// range through the list and return secret content
	for _, secretName := range originalList {

		// get a k8s secret based on namespace and name
		secretContent, _ := c.getKubernetesSecret(*namespace, secretName)

		// this returns a map of the secret object which can contain multiple files, as a result, let's parse through each one and create as needed
		for objName, objData := range secretContent.Data {

			// SKIP IF:
			// objName key is "namespace" since we provide this  each run (--namespace, default is "default")
			// objName key is "token" as it's generally a random service account token (potentially could override later)
			// objName key is "ca.crt" as it's usually the default CA (commonly not real application secrets)
			if cmp.Equal(objName, "namespace") || cmp.Equal(objName, "token") || cmp.Equal(objName, "ca.crt") {
				if *debug {
					log.Printf("Skipped adding secret object ['%s'] to list by default", objName)
				}
				continue
			}

			// Announce what's happening, if not deleting
			if *delete {
				log.Printf("🚫 Deleting secret object(s) for ['%s']\n", secretContent.ObjectMeta.Name)
			} else {
				log.Printf("✅ Migrating secret object(s) for ['%s']\n", secretContent.ObjectMeta.Name)
			}

			// replace periods with dashes and create a new safe name [SECRETNAME]-[OBJECTKEYNAME]
			safeSecretName := fmt.Sprintf(strings.Replace(secretName, ".", "-", -1) + "-" + strings.Replace(objName, ".", "-", -1))

			// build the array
			safeList = append(safeList, safeSecretName)

			// Output what's being deleted
			if *delete {
				c.deleteSecret(safeSecretName)
				log.Printf("  - Deleted secret named ['%s'] in GCP project: ['%s'] \n", safeSecretName, *project)
				continue
			}

			// Output more information on things being created
			if *debug {
				log.Printf("Running createGoogleSecret() for Kubernetes secret ['%s'], from ['%s'] namespace, named ['%s'] in Google project ['%s'], using data from the ['%s'] object key.\n", secretContent.ObjectMeta.Name, *namespace, safeSecretName, *project, objName)
			}
			c.createGoogleSecret(safeSecretName, objName, objData)
			log.Printf("  - Created secret named ['%s'] in GCP project: ['%s']\n", safeSecretName, *project)

		}
	}

	// list after cleaning
	finalList, _ := json.Marshal(safeList)
	// display the secret names modified without periods for record
	log.Printf("📋 SafeName List: %v\n", string(finalList))

}

func (c *Client) deleteSecret(mySecretName string) error {
	// create the request with secret name `projects/*/secrets/*`.
	req := &secretmanagerpb.DeleteSecretRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s", *project, mySecretName),
	}

	// Delete it!
	if err := c.gsmClient.DeleteSecret(context.TODO(), req); err != nil {
		return fmt.Errorf("failed to delete secret: %v", err)
	}
	return nil
}

func (c *Client) createGoogleSecret(myGCPSecretName string, mySecretK8sName string, mySecretDataValue []byte) {
	// build the the request to create the secret (--project for *project).
	createSecretReq := &secretmanagerpb.CreateSecretRequest{
		Parent: fmt.Sprintf("projects/%s", *project),
		// create oldname-datakey (since there might be multiple named.)
		SecretId: myGCPSecretName,
		Secret: &secretmanagerpb.Secret{
			// Labels: map[string]string{"oldk8sname": mySecretK8sName, "testdemokey": "testdemovalue"},
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
		},
	}

	// create the empty secret "template" using the defined request
	secret, err := c.gsmClient.CreateSecret(context.TODO(), createSecretReq)
	if err != nil {
		log.Fatalf("failed to create secret: %v", err)
	}

	// declare the payload to store (NEEDS TO BE BYTES)
	payload := mySecretDataValue

	// build the request
	addSecretVersionReq := &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret.Name,
		Payload: &secretmanagerpb.SecretPayload{
			Data: payload,
		},
	}

	// call the API to get the version (or n+1) and create the define the request
	// AddSecretVersion creates a new SecretVersion containing secret data and attaches it to an existing Secret.
	resp, err := c.gsmClient.AddSecretVersion(context.TODO(), addSecretVersionReq)
	if err != nil {
		log.Fatalf("failed to add secret version: %v", err)
	}

	// TODO: Use resp
	_ = resp

}

// get secretS (list) from a Kubernetes namespace
func (c *Client) getKubernetesSecrets(namespace string) (*corev1.SecretList, error) {

	sl, err := c.k8sClient.CoreV1().Secrets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("some context: %v", err)
	}
	return sl, err
}

// get a secret's data from named string
func (c *Client) getKubernetesSecret(namespace string, name string) (*corev1.Secret, error) {
	s, err := c.k8sClient.CoreV1().Secrets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("some context: %v", err)
	}
	return s, err
}

// takes a (secrets) list and a (exclude) string
func remove[T comparable](l []T, item T) []T {
	out := make([]T, 0)
	for _, element := range l {
		// if the (secrets) list does not contain (exclude) item
		if element != item {
			// rebuild a new slice only containing non-excluded values
			out = append(out, element)
		}
	}
	return out
}
