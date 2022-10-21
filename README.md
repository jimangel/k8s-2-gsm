`k8s-2-gsm` is a containerized script that leverages the Google and Kubernetes API clients to migrate Kubernetes secrets to Google Secret Manager.

If migrating GKE secrets to Google Secret Manager, Workload Identity can simplify the process/automation.

## Prerequisites

- A Kubernetes cluster with the ability to edit RBAC
  - An active `kubectl` client or API access
- Google Project with billing enabled & owner access

If not enabled, enable the following GCP APIs:

```shell
gcloud services enable container.googleapis.com
gcloud services enable secretmanager.googleapis.com
```

> TODO: add picture (IAM/bindings in GCP vs. GKE/K8S to k/v maps)

## Known limitations

- Google Secret Manager is a key/value store, meaning only one secret per object.
  - Example: If you had 1 Kubernetes secret with 2 objects (tls.crt / tls.key), you now need 2 Google Secret Manager secrets (and 2 references / mounts in yaml).

    ![text view of secret yaml data](pics/secret-2-items.png)

    > I need to double-check how the CSI driver spec handles this.

- The values/file [payload size must be no larger than 64 KiB](https://cloud.google.com/secret-manager/quotas#content_limits)
  - Kubernetes secret [size limit is 1MiB](https://kubernetes.io/docs/concepts/configuration/secret/#restriction-data-size)

## Quickstart

The application attempts to act as a script and is a single file (main.go). The container is built with `KO` which publishes a multi-arch image to `us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration` with the ``

Arguments, flags, and options.

```shell
# go run . --help
Usage of ./demo:
  -delete --delete
    	If set --delete or `--delete=True` the Google secrets are deleted. If the secrets exist, the program breaks - on purpose
  -exclude string
    	Name of secrets to exclude, comma delimited, default: ''
  -namespace string
    	Name of the namespace to look for secrets, default: default (default "default")
  -project string
    	Name of GCP project to migrate secrets, default: ''
```

```shell
# refresh GCP creds. This is the account / ADC used for managing secrets
# refresh GCP creds without a browser and no prompt
gcloud auth application-default login --no-launch-browser --quiet

# the future is here, give up in-tree plugins
export USE_GKE_GCLOUD_AUTH_PLUGIN=True # consider adding to /etc/.profile

# export GKE_NAME="sandbox-gke"
gcloud container clusters get-credentials ${GKE_NAME}
# TEST: `kuebctl get nodes -A`

# run `--help` from application command (to display all options). Should not perform any action.
sudo docker run \
-u $(id -u) \
-v ${HOME}/.kube/config:/.kube/config:rw \
-e GOOGLE_APPLICATION_CREDENTIALS=/gcp/creds.json \
-e USE_GKE_GCLOUD_AUTH_PLUGIN=True \
-v ${HOME}/.config/gcloud/application_default_credentials.json:/gcp/creds.json:ro \
us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration:1.0.0-alpha \
--help
```

Output similar to:

```shell
Usage of /ko-app/demo:
  -delete --delete
    	If set --delete or `--delete=True` the Google secrets are deleted. If the secrets exist, the program breaks - on purpose
  -exclude string
    	Name of secrets to exclude, comma delimited, default: ''
  -namespace string
    	Name of the namespace to look for secrets, default: default (default "default")
  -project string
    	Name of GCP project to migrate secrets, default: ''
```

Set ENV vars (`NAMESPACE`, `PROJECT_ID/NAME`), $NAMESPACE non-default secrets to $PROJECT_ID Google Secret Manager:

```shell
export NAMESPACE="app-team-a"
export PROJECT_ID="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"
```

> Optionally, in the following sections, pass a `--exclude="name-or-key-1,name-or-key-2,name-or-key-3,name-or-key-4,name-or-key-5"`

```shell
# create/migrate secrets from a namespace  (${NAMESPACE}) to GSM (using passed in project arg)
export NAMESPACE="app-team-a"
export PROJECT="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"

# if outside the cluster, set the kubeconfig location
export KUBECONFIG="${HOME}/.kube/config"

# update / create ADC credentials or use a Google service account
gcloud auth application-default login

# scan ${NAMESPACE} for non-default secrets and migrate to ${PROJECT_ID} if found
# 
sudo docker run \
-u $(id -u) \
-v ${KUBECONFIG}:/.kube/config:rw \
-e USE_GKE_GCLOUD_AUTH_PLUGIN=True \
-e GOOGLE_APPLICATION_CREDENTIALS=/gcp/creds.json \
-v ${HOME}/.config/gcloud/application_default_credentials.json:/gcp/creds.json:ro \
us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration:1.0.0-alpha \
-- /ko-app/demo --project=${PROJECT_ID} --namespace=${NAMESPACE}



# /ko-app/demo
# delete secrets from GSM
# go run . --project=${PROJECT_ID} --namespace=${NAMESPACE}



# GKE ONLY: since I'm testing with docker + external kubeconfig, I need to mount the gke-gcloud-auth-plugin.
sudo docker run \
-u $(id -u) \
-v ${KUBECONFIG}:/.kube/config:rw \
-v $(which gke-gcloud-auth-plugin):$(which gke-gcloud-auth-plugin) \
-v $(which gcloud):$(which gcloud) \
-e USE_GKE_GCLOUD_AUTH_PLUGIN=True \
-e GOOGLE_APPLICATION_CREDENTIALS=/gcp/creds.json \
-v ${HOME}/.config/gcloud/application_default_credentials.json:/gcp/creds.json:ro \
us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration:1.0.0-alpha \
-- /ko-app/demo --project=${PROJECT_ID} --namespace=${NAMESPACE} --delete
```

## Running the containerized script

If running in a GKE cluster, Workload Identity provides credentials. If testing outside of the cluster, you can log in and refresh your local credentials with:

### Update / refresh gcloud local credentials

```shell

```

### GKE >=v1.25 Auth plugin disclaimer

Also, if running outside the cluster, ensure you have the `gke-gcloud-auth-plugin` auth plugin installed too (example: `gcloud components install gke-gcloud-auth-plugin`)

### Google Secret manager setup

Not much is needed as Google's Secret Manager is generally flat structured. I'll confirm this as I learn more.

TODO

### CSI installed

> https://www.youtube.com/watch?v=w0k7MI6sCJg

- TODO
- https://secrets-store-csi-driver.sigs.k8s.io/getting-started/installation.html

```shell
# brew upgrade helm
helm repo add secrets-store-csi-driver https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts
helm repo update

# install the latest csi driver, TODO add note about releases
helm install csi secrets-store-csi-driver/secrets-store-csi-driver --namespace kube-system

# install the CSI plugin for GCP on K8S/GKE
cd ~/
git clone git@github.com:GoogleCloudPlatform/secrets-store-csi-driver-provider-gcp.git
cd ./secrets-store-csi-driver-provider-gcp
helm upgrade --install secrets-store-csi-driver-provider-gcp charts/secrets-store-csi-driver-provider-gcp
```

### (optional) GKE workload identity setup

#### GCP Setup

> NOTE: These instructions may be partial / incomplete

Create the Service Account in GCP. The name does not need to match (k8s <-> gcp)

```shell
export SA_NAME="wi-gsm"
export PROJECT_ID="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"
gcloud iam service-accounts create ${SA_NAME} --project=${PROJECT_ID}
```

Bind the Service Account to use Google Secret Manager. I'll use admin (`roles/secretmanager.admin`) but we should scope down or creating a custom role.

```shell
# grant the service account IAM bindings
gcloud projects add-iam-policy-binding ${PROJECT_ID} \
--member "serviceAccount:${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
--role "roles/secretmanager.admin"

# for the day-2-day use, maybe switch to "roles/secretmanager.secretAccessor"?
```

#### Kubernetes Setup

Grant k8s impersonation of the `secret-grabber` Kubernetes named service account in the "default" named namespace.

```shell
# set the k8s vars
export K8S_SERVICEACCOUNT="secret-grabber"
export K8S_NAMESPACE="default"

# gcp iam binding allowing a named service account (and namespace) to leverage workload identity
gcloud iam service-accounts add-iam-policy-binding ${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com \
--role roles/iam.workloadIdentityUser \
--member "serviceAccount:${PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SERVICEACCOUNT}]"

# create k8s service account
kubectl -n ${K8S_NAMESPACE} create serviceaccount ${K8S_SERVICEACCOUNT}

# annotate GKE's service account to link to the GCP identity
kubectl annotate serviceaccount ${K8S_SERVICEACCOUNT} \
--namespace ${K8S_NAMESPACE} \
iam.gke.io/gcp-service-account=${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com
```

### Run the job in cluster with workload identity

```shell
export FULL_IMAGE_PATH_W_TAG="us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration:1.0.0-alpha"

# TODO
# kubectl create job --image=${FULL_IMAGE_PATH_W_TAG} secret-migration -- "/ko-app/app --project gke-gsm-migration-sandbox"
# kubectl run test --image=${FULL_IMAGE_PATH_W_TAG} --restart=Never -it --rm -- /ko-app/demo
```

### Run on Kubernetes (GKE)

> Where does the app get it's credentials?
> - ADC
> - WI
> TODO

This guide starts on GKE but most of it should be portable assuming the container (or script) has some form of ADC and there's some form of kubeconfig passed to app.

There's a cluster role included limited to secrets, allowing for us to use role bindings when setting up our account.

> TODO: https://github.com/boredabdel/gke-secret-manager/blob/main/hello-secret-csi-driver/k8s-manifest.yaml

```shell

export PROJECT_ID="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"

cat <<EOF | kubectl apply -f -
apiVersion: secrets-store.csi.x-k8s.io/v1alpha1
kind: SecretProviderClass
metadata:
  name: app-secrets
  namespace: ${K8S_NAMESPACE}
spec:
  provider: gcp
  parameters:
    secrets: |
      - resourceName: "projects/${PROJECT_ID}/secrets/${SECRET_NAME}/versions/${SECRET_VERSION}"
        fileName: "good1.txt"
---
apiVersion: v1
kind: Pod
metadata:
  name: mypod
  namespace: default
spec:
  serviceAccountName: secret-ksa
  containers:
  - image: gcr.io/google.com/cloudsdktool/cloud-sdk:slim
    name: mypod
    resources:
      requests:
        cpu: 100m
    tty: true
    volumeMounts:
      - mountPath: "/var/secrets"
        name: mysecret
  volumes:
  - name: mysecret
    csi:
      driver: secrets-store.csi.k8s.io
      readOnly: true
      volumeAttributes:
        secretProviderClass: "app-secrets"
EOF
```


```shell
cat <<EOF | kubectl -n ${K8S_NAMESPACE} apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: my-secret-clusterrole
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "watch", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: read-secrets-${K8S_NAMESPACE}
subjects:
- kind: ServiceAccount
  name: ${K8S_SERVICEACCOUNT} # name of your service account
  namespace: ${K8S_NAMESPACE}
roleRef: # referring to your ClusterRole
  kind: ClusterRole
  name: my-secret-clusterrole
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: batch/v1
kind: Job
metadata:
  name: k8s-2-gsm
  namespace: ${K8S_NAMESPACE}
spec:
  template:
    spec:
      serviceAccountName: ${K8S_SERVICEACCOUNT}
      containers:
      - name: k8s-secret-migration
        image: ${FULL_IMAGE_PATH_W_TAG}
        command: [/ko-app/demo,  --project=gke-gsm-migration-sandbox, --namespace=${K8S_NAMESPACE}]
        imagePullPolicy: Always
      restartPolicy: Never
  backoffLimit: 0
EOF
```

Debug by getting the logs:

```shell
# careful if you have lots of jobs
kubectl -n ${K8S_NAMESPACE} logs job/$(kubectl get job -o=jsonpath="{.items[*]['metadata.name']}")
```

### Clean up (WARNING: this deletes ALL completed jobs):

```shell
# https://stackoverflow.com/questions/53539576/kubectl-list-delete-all-completed-jobs
kubectl delete -n ${K8S_NAMESPACE} job $(kubectl -n ${K8S_NAMESPACE} get job -o=jsonpath="{.items[*]['metadata.name']}")
```

## Quickstart Demo

1. create k8s new namespace and secret

    ```shell
    # set the k8s namespace vars
    export K8S_NAMESPACE="new-fancy-test"

    kubectl create ns ${K8S_NAMESPACE}

    kubectl -n ${K8S_NAMESPACE} create secret generic demo-incluster --from-literal=secretKey=secretValue
    ```

1. create k8s deployment using existing Kubernetes secret

```shell
# from https://www.alibabacloud.com/blog/how-to-create-and-use-secrets-in-kubernetes_594723
cat <<EOF | kubectl -n ${K8S_NAMESPACE} apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: my-secret-pod
spec:
  containers:
  - name: my-secret-pod
    image: alpine
    imagePullPolicy: IfNotPresent
    
    tty: true
    stdin: true
       
    command: ["/bin/ash", "-ec", "while :; do echo '.'; sleep 15 ; done"]

    volumeMounts:
    - name: my-volume
      mountPath: "/etc/secret"
      readOnly: true
      
  volumes:
  - name: my-volume
    secret:
      secretName: demo-incluster
EOF

# explore via ssh into the container
kubectl -n ${K8S_NAMESPACE} exec my-secret-pod -it -- /bin/ash
# don't forget to exit
exit

# cleanup
kubectl -n ${K8S_NAMESPACE} delete pod my-secret-pod
```
    

1. run migration script

```shell
cat <<EOF | kubectl apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: my-secret-clusterrole
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "watch", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: read-secrets-${K8S_NAMESPACE}
subjects:
- kind: ServiceAccount
  name: ${K8S_SERVICEACCOUNT} # name of your service account
  namespace: ${K8S_NAMESPACE}
roleRef: # referring to your ClusterRole
  kind: ClusterRole
  name: my-secret-clusterrole
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: batch/v1
kind: Job
metadata:
  name: k8s-2-gsm
spec:
  template:
    spec:
      serviceAccountName: ${K8S_SERVICEACCOUNT}
      containers:
      - name: k8s-secret-migration
        image: ${FULL_IMAGE_PATH_W_TAG}
        command: ['/ko-app/demo',  '--project=gke-gsm-migration-sandbox', '--namespace=${K8S_NAMESPACE}']
        imagePullPolicy: Always
      restartPolicy: Never
  backoffLimit: 0
EOF
```

check the logs

```shell

```

```shell
# careful if you have lots of jobs
# TODO: Might not work
kubectl -n ${K8S_NAMESPACE} logs job/$(kubectl -n ${K8S_NAMESPACE} get job -o=jsonpath="{.items[*]['metadata.name']}")
```


1. (demo/manually) update to use Google Secret Manager / CSI

    > https://github.com/boredabdel/gke-secret-manager
    > SecretProviderClass == https://github.com/GoogleCloudPlatform/secrets-store-csi-driver-provider-gcp/blob/main/examples/app-secrets.yaml.tmpl
    > Pod == https://github.com/GoogleCloudPlatform/secrets-store-csi-driver-provider-gcp/blob/main/examples/mypod.yaml.tmpl


1. update deployments and test

```shell
TODO

# troubleshooting
kubectl exec demo-bad -it -- bash
```

## Development work

### CI

TODO (if needed)

Setting up Identity Federation for GitHub Actions

```shell
# export PROJECT_ID=[...]

gcloud auth login

gcloud config set project ${PROJECT_ID}
```

TODO

- cover creating service account and IAM binding for secret admin / artifactory admin...


```shell
gcloud iam workload-identity-pools create "gh-keyless" \
  --project="${PROJECT_ID}" \
  --location="global" \
  --display-name="Pool for GitHub Action ID"
```

Create an oidc provider for your actions, including an assertion based on repo.

```shell
gcloud iam workload-identity-pools providers create-oidc "my-gha-provider" \
  --project="${PROJECT_ID}" \
  --location="global" \
  --workload-identity-pool="gh-keyless" \
  --display-name="My GHA provider" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --issuer-uri="https://token.actions.githubusercontent.com"
```

Finally, create and allow authentications from the Workload Identity Provider to impersonate the desired Service Account

```shell
# ABILITY TO PUSH IMAGES (OR FUTURE IAM) LIMITED TO ${GH_REPO_PATH}
export GH_REPO_PATH=jimangel/k8s-2-gsm

# run from another project
export PROJECT_ID="jimangel"

# get actual ID of project
export WI_PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")

# attribute.repository/${GH_REPO_PATH} limits who can use workload identity and 
gcloud iam service-accounts add-iam-policy-binding "gha-keyless-sa-4-gar@${PROJECT_ID}.iam.gserviceaccount.com" \
  --project="${PROJECT_ID}" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/projects/${WI_PROJECT_NUMBER}/locations/global/workloadIdentityPools/gh-keyless/attribute.repository/${GH_REPO_PATH}"
```


### Run locally using Application Default Credentials (ADC) and a local kubeconfig

With golang (no container).

> Where does the app get it's credentials?
> - ADC
> - WI
> TODO

```shell
# Export the dynamic variables for the migration tool. If --delete is not passed, it assumes create. The application does NOT overwrite any existing secrets
export NAMESPACE="default"
export PROJECT_ID="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"

# refresh GCP creds
gcloud auth login --no-launch-browser --update-adc --quiet

# create / copy k8s secrets to project gsm (implied default namespace)
go run . --project=${PROJECT_ID} 

# delete GSM secrets
go run . --project=${PROJECT_ID} --delete
```

### Run in a container

> Where does the app get it's credentials?
> - ADC
> - WI
> TODO

TODO

```shell
# ... docker run -e ENV='dev' -p 8080:8080 --name demo-app ${FULL_IMAGE_PATH_W_TAG}
```

### Future / TODO

- Include the ability to generate YAML to update deployments
- The k8s YAML could be a one-liner kubectl command if the API reference was looked up
- Check TODO in main.go
- double check example
- for the day-2-day use, maybe switch to "roles/secretmanager.secretAccessor"?

### Troubleshooting

> failed to define/gather list secret names: secrets is forbidden: User "system:serviceaccount:default:secret-grabber" cannot list resource "secrets" in API group "" in the namespace "default"

First, check that the service account is the correct one. Then ensure there is some form of RBAC binding allowing GET/WATCH/LIST on the objects. See [Checking API Access](https://kubernetes.io/docs/reference/access-authn-authz/authorization/#checking-api-access):

```shell
# grep for secrets: `| grep secrets` and look for [get watch list]
kubectl auth can-i --list --namespace ${NAMESPACE} --as system:serviceaccount:${NAMESPACE}:${K8S_SERVICEACCOUNT}
```

Check Workload Identity configuration. Check the service account with: `kubectl describe -n ${NAMESPACE} sa ${K8S_SERVICEACCOUNT}`. If you see the following:

```shell
Name:                default
Namespace:           default
Labels:              <none>
Annotations:         <none> # <----- There's the problem!
Image pull secrets:  <none>
Mountable secrets:   default-token-ltrnm
Tokens:              default-token-ltrnm
Events:              <none>
```

There isn't an annotation for [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#authenticating_to).

```shell
# check image size (~47MB at time of writing)
docker pull ${FULL_IMAGE_PATH_W_TAG} && docker images ${FULL_IMAGE_PATH_W_TAG} --format='{{.Size}}'

# other debuging
# kubectl -n ${K8S_NAMESPACE} describe pod my-secret-pod
# kubectl -n ${K8S_NAMESPACE} get pod my-secret-pod -o yaml
# kubectl get events --sort-by='.lastTimestamp' -n ${K8S_NAMESPACE}
```