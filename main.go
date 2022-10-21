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
	"flag"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1Types "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Required for GKE client-go - https://github.com/kubernetes/client-go/issues/242#issuecomment-314642965
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	// secret manager stuff
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// TODO:
// - figure out naming and feedback
// - test multiple data migration types (TLS, data, files, etc)
// - secrets right now are parsed one at a time, need to bundle by name
// - need to validate TLS validity after migration via keypair check
// - define include / exclude flow and process
// - output csv (create a map/struct and io dump)
// - sample deployment YAML generation? (might be another tool)
// - maybe add namespace to labels
// - add a --debug that dumps the payloads?
// - allow a skip if the secret exists vs fail in the function?
// - convert delete bool to be if / elseif (vs 2x if)
// - move README.md steps to make / script

var (
	namespace            = flag.String("namespace", "default", "Name of the namespace to look for secrets, default: default")
	exclude              = flag.String("exclude", "", "Name of secrets to exclude, comma delimited, default: ''")
	project              = flag.String("project", "", "Name of GCP project to migrate secrets, default: ''")
	delete               = flag.Bool("delete", false, "If set `--delete` or `--delete=True` the Google secrets are deleted. If the secrets exist, the program breaks - on purpose")
	secretsClient        corev1Types.SecretInterface
	excludedSecrets      = make(map[string]struct{})
	nonAlphanumericRegex = regexp.MustCompile(`[^a-z-A-Z-0-9- ]+`) // generate a Google Secret Manager friendly name (mainly an issue with periods. It looks funny because there's a `-` to the right of each group to allow for dashes.)
	trap                 = 0                                       // trap variable to see if anything was found (if incremented, _something_ was done)
	status               = ""
)

func main() {
	// check args
	flag.Parse()

	// get a clientset for k8s from the getClientset func
	clientset, err := getClientset()
	if err != nil {
		log.Fatalf("failed to create the clientset for k8s (kubectl): %v", err)
	}

	// create the client interface for secrets
	secretsClient := clientset.CoreV1().Secrets(*namespace)

	// define list of secret names from an API call
	secretList, err := secretsClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("failed to define/gather list secret names: %v", err)
	}

	// for each secret, run our secret-parsing function
	for key, secret := range secretList.Items {

		loopK8sSecretData(secret.Name)

		// check if we're at the end of the for loop range (-1 compensates for the 0 index range)
		// also check if any action incremented our trap variable
		if key == len(secretList.Items)-1 && trap == 0 {
			fmt.Printf("After all filters, no secrets found in '%s' namespace ('%s'): Try Again.\n", *namespace, *project)
		}
	}
}

// dynamically get incluster config first or default to kubeconfig file (https://discuss.kubernetes.io/t/golang-client-api-clientset-inside-cluster-vs-outside-of-cluster/16697/3)
func getClientset() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig :=
			clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(config)
}

// parse / read the specified secret and print its data
func loopK8sSecretData(name string) {

	// parse and create the secret exclusion map struct from input
	excludeSplit := strings.Split(*exclude, ",")
	for _, secret := range excludeSplit {
		excludedSecrets[secret] = struct{}{}
	}

	// get a clientset for k8s from the getClientset func
	clientset, err := getClientset()
	if err != nil {
		log.Fatalf("failed to create the clientset for k8s (kubectl): %v", err)
	}

	// create the client interface for secrets
	secretsClient := clientset.CoreV1().Secrets(*namespace)

	// using the client interface get the k8s secret k/v data
	secret, err := secretsClient.Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("failed to get the k8s secret k/v data: %v", err)
	}

	// when reading a secret, only Data contains any data, StringData is empty (https://pkg.go.dev/k8s.io/api/core/v1#Secret)
	for key, value := range secret.Data {

		inputSecretName := secret.ObjectMeta.Name // get the name from the secret

		// skip if the secret's name matches any excluded string(s)
		if _, ok := excludedSecrets[inputSecretName]; ok {
			continue
		}

		// SKIP IF:
		// secret.Data key is "namespace" since we provide this  each run (--namespace, default is "default")
		// secret.Data key is "token" as it's generally a random service account token (potentially could override later)
		// secret.Data key is "ca.crt" as it's usually the default CA (commonly not real application secrets)
		// inputSecretName starts with a "default-token-*" prefix indicating it's the default service account
		if cmp.Equal(key, "namespace") || cmp.Equal(key, "token") || cmp.Equal(key, "ca.crt") || strings.HasPrefix(inputSecretName, "default-token-") {
			// If matches are found skip to the next key/value
			continue
		}

		// create a secret-friendly name with various format parsing
		safeSecretName := fmt.Sprintf(inputSecretName + "-" + key)
		// the last argument (-1) is set so there is no limit on the number of replacements. By doing it here, it preserves secrets like tls.crt into tls-crt (for the next step)
		safeSecretName = strings.Replace(safeSecretName, ".", "-", -1)
		// replace all nonAlphanumeric chars so it meet's Google's standards
		safeSecretName = nonAlphanumericRegex.ReplaceAllString(safeSecretName, "")

		// if `--delete` flag is set, delete all found k8s secrets in Google
		if *delete {

			status := "delete"

			//fmt.Printf("TESTING: %v\n", key)

			if trap < 1 {
				fmt.Printf("OUTPUT (%s):\n-----------------\n", status)
				fmt.Printf("k8s_secret_name,google_secret_name,gcp_project,k8s_namespace,secret_key_name\n")
			}
			// increment trap if anything happens
			*&trap++
			// throw a header on it if we're here (runs 1 time)
			// if delete, then were running the "create" operation
			fmt.Printf("%s,%s,%s,%s,\"%s\"\n", inputSecretName, safeSecretName, *project, *namespace, key)
			deleteGoogleSecret(safeSecretName)
			// continue skips the rest of the loop without creating anything new (sneaky!)
			continue
		}

		// if NOT (!) --delete flag is set, assumes a "create" is happening and generates a header:
		if !*delete {

			status = "create"

			if trap < 1 {
				fmt.Printf("OUTPUT (%s):\n-----------------\n", status)
				fmt.Printf("k8s_secret_name,google_secret_name,gcp_project,k8s_namespace,secret_key_name\n")
			}

			// increment trap if anything happens
			*&trap++

			// throw a header on it if we're here (runs 1 time)
			if trap >= 1 {

				// shoutout what the script is doing (if not delete, then were running the "create" operation)
				fmt.Printf("%s,%s,%s,%s,\"%s\"\n", inputSecretName, safeSecretName, *project, *namespace, key)

				// create secret via functions below
				createGoogleSecret(safeSecretName, inputSecretName, value)

			}

			// DEBUG: Read secret via functions below
			// readGoogleSecret(safeSecretName)
		}
	}
}

// INPUTS:
// myGCPSecretName == safeSecretName
// mySecretK8sName == old secret name // used for labels
// mySecretDataValue == PAYLOAD       // (secret data as bytes)
func createGoogleSecret(myGCPSecretName string, mySecretK8sName string, mySecretDataValue []byte) {

	// create the context and client
	ctx := context.TODO()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("failed to setup client: %v", err)
	}

	// defer closing the client context connection until the remaining programs (in the function) finish
	defer client.Close()

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
	secret, err := client.CreateSecret(ctx, createSecretReq)
	if err != nil {
		log.Fatalf("failed to create secret: %v", err)
	}

	// Declare the payload to store (NEEDS TO BE BYTES)
	payload := mySecretDataValue

	// Build the request
	addSecretVersionReq := &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret.Name,
		Payload: &secretmanagerpb.SecretPayload{
			Data: payload,
		},
	}

	// Call the API to get the version (or n+1) and create the define the request
	// AddSecretVersion creates a new SecretVersion containing secret data and attaches it to an existing Secret.
	resp, err := client.AddSecretVersion(ctx, addSecretVersionReq)
	if err != nil {
		log.Fatalf("failed to add secret version: %v", err)
	}

	// TODO: Use resp
	_ = resp

}

func readGoogleSecret(mySecretName string) {

	// create the context and client
	ctx := context.TODO()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("failed to setup client: %v", err)
	}

	// defer closing the client context connection until the remaining programs (in the function) finish
	defer client.Close()

	// Read the data from Google
	// projects/*/secrets/*/versions/latest is an alias to the most recently created SecretVersion.
	// Build the request.
	accessRequest := &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest", *project, mySecretName),
	}

	//result, err := client.AccessSecretVersion(ctx, accessRequest)
	result, err := client.AccessSecretVersion(ctx, accessRequest)
	if err != nil {
		log.Fatalf("failed to access secret version: %v", err)
	}

	// DEBUG: Print the secret payload.
	//
	// WARNING: Do not print the secret in a production environment - this
	// snippet is showing how to access the secret material.
	// log.Printf("Plaintext: %s", result.Payload.Data)
	_ = result

}

func deleteGoogleSecret(mySecretName string) error {

	// create the context and client
	ctx := context.TODO()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("failed to setup client: %v", err)
	}

	// defer closing the client context connection until the remaining programs (in the function) finish
	defer client.Close()

	// create the request with secret name
	// `projects/*/secrets/*`.
	req := &secretmanagerpb.DeleteSecretRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s", *project, mySecretName),
	}

	// Delete it!
	if err := client.DeleteSecret(ctx, req); err != nil {
		return fmt.Errorf("failed to delete secret: %v", err)
	}
	return nil
}
