# Real world demo of swapping out secrets

## Pre-reqs

A GKE cluster with [Workload Identity enabled](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) (configuration is partially covered below). For example:

```
gcloud compute networks create gke-vpc-demo-test --subnet-mode=custom
export GKE_NAME="new-sandbox-gke"
export GCP_PROJECT="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"
gcloud container clusters create ${GKE_NAME} --network=gke-vpc-demo-test --num-nodes 2 --machine-type e2-standard-4 --enable-ip-alias --create-subnetwork name=gke-subnet-0 --workload-pool=${GCP_PROJECT}.svc.id.goog
```

## Cluster setup using k8s secrets

First we need something running to migrate. Setup a namespace with a deployment that uses secrets:

```
# define the namespace
export K8S_NAMESPACE="test-new"

# create namespace & context
kubectl create namespace ${NAMESPACE}
kubectl config set-context --current --namespace=${NAMESPACE}

# create secrets
kubectl create secret generic literal-token --from-literal user=admin --from-literal password=1234

# create deployment
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: secret-demo
spec:
  containers:
  - name: busybox
    image: busybox:1.34
    args:
    - sleep
    - "1000000"
    volumeMounts:
    - name: secret-data
      mountPath: "/etc/secrets"
      readOnly: true
  volumes:
  - name: secret-data
    secret:
      secretName: literal-token
EOF
```

Validate the deployment / secrets exist:

```
# exec into the pod
kubectl exec -it secret-demo -- sh

# list the secrets
ls -lh /etc/secrets
```

Output looks like:

```
ls -lah /etc/secrets
total 0      
lrwxrwxrwx    1 root     root          15 Nov  8 15:42 password -> ..data/password
lrwxrwxrwx    1 root     root          11 Nov  8 15:42 user -> ..data/user
```

Lastly, check the file contents:

```
# should return "admin"
cat /etc/secrets/user
```

## Migrate the secrets

Create the [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity) configuration allowing the job to have the required permission to migrate secrets.

High-level steps:
- Set variables for service account names (in GCP & Kubernetes) and project settings
- Create a (GCP) service account in Google
- Bind `roles/secretmanager.admin` IAM to the GCP service account for secret creation
- Bind `roles/iam.workloadIdentityUser` to the service account
- Create a Kubernetes service account and association with the GCP service account
- Run the containerized script / job

### Setup GCP

Set variables for service account names (in GCP & Kubernetes) and project settings

```
export GCP_PROJECT="$(gcloud config list --format 'value(core.project)' 2>/dev/null)"
export GCP_SERVICEACCOUNT="k8s-2-gsm-wi"
export K8S_SERVICEACCOUNT="secret-mover"
export K8S_NAMESPACE="test-new"
```

Create a (GCP) service account in Google

```
gcloud iam service-accounts create ${GCP_SERVICEACCOUNT} --project=${GCP_PROJECT}
```

Bind `roles/iam.workloadIdentityUser` IAM to a Kubernetes service account (and namespace)

```
gcloud iam service-accounts add-iam-policy-binding ${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com \
--role roles/iam.workloadIdentityUser \
--member "serviceAccount:${GCP_PROJECT}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SERVICEACCOUNT}]"
```

Bind `roles/secretmanager.admin` IAM to the GCP service account for secret creation

```
gcloud projects add-iam-policy-binding ${GCP_PROJECT} \
--member "serviceAccount:${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com" \
--role "roles/secretmanager.admin"
```

At this point, GCP permissions are set and now we need to configure Kubernetes to "link" the service accounts for Workload Identity

### Setup Kubernetes

Create a Kubernetes service account and association with the GCP service account

```
# create k8s service account
kubectl -n ${K8S_NAMESPACE} create serviceaccount ${K8S_SERVICEACCOUNT}

# annotate GKE's service account to link to the GCP identity
kubectl annotate serviceaccount ${K8S_SERVICEACCOUNT} \
--namespace ${K8S_NAMESPACE} \
iam.gke.io/gcp-service-account=${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com
```

Grant the service account the ability to read secrets in the namespace

```
# create a reusable cluster role allowing to read secrets
kubectl create clusterrole secret-reader --verb=get,list,watch --resource=secrets

# bind the cluster role to our Workload Identity service account
kubectl -n ${K8S_NAMESPACE} create rolebinding read-secrets-${K8S_NAMESPACE} --clusterrole=secret-reader --serviceaccount=${K8S_NAMESPACE}:${K8S_SERVICEACCOUNT}
```

Validate access:

```
kubectl auth can-i get secrets --namespace ${NAMESPACE} --as system:serviceaccount:${NAMESPACE}:${K8S_SERVICEACCOUNT}

# yes
```

### Run the migration container

Get the latest tag / sha from [the repo](https://us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration)

```
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-secrets
spec:
  backoffLimit: 0  # run once
  template:
    spec:
      containers:
      - image: us-central1-docker.pkg.dev/jimangel/public-repo/secret-migration:1.0.0-alpha
        name: migrate-secrets
        args:
        - --project=${GCP_PROJECT}
        - --namespace=${K8S_NAMESPACE}
      restartPolicy: Never
      serviceAccountName: ${K8S_SERVICEACCOUNT}
EOF
```

Watch logs:

```
kubectl logs -f job/migrate-secrets
```

### Install [Secret Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/getting-started/installation.html)

I'm following the [yaml instructions](https://secrets-store-csi-driver.sigs.k8s.io/getting-started/installation.html#alternatively-deployment-using-yamls) to avoid installing Helm.

```
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/secrets-store-csi-driver/main/deploy/rbac-secretproviderclass.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/secrets-store-csi-driver/main/deploy/csidriver.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/secrets-store-csi-driver/main/deploy/secrets-store.csi.x-k8s.io_secretproviderclasses.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/secrets-store-csi-driver/main/deploy/secrets-store.csi.x-k8s.io_secretproviderclasspodstatuses.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/secrets-store-csi-driver/main/deploy/secrets-store-csi-driver.yaml
```

Ensure it's running:

```
kubectl get po --namespace=kube-system
kubectl get crd
```

### Install [Google Secret Manager Provider](https://github.com/GoogleCloudPlatform/secrets-store-csi-driver-provider-gcp) for Secret Store CSI Driver

```
kubectl apply -f https://raw.githubusercontent.com/GoogleCloudPlatform/secrets-store-csi-driver-provider-gcp/main/deploy/provider-gcp-plugin.yaml
```

### Setup an application service account (in GCP & k8s) for using Google secrets

```
export GCP_WORKLOAD_SA=gke-workload-1

gcloud iam service-accounts create ${GCP_WORKLOAD_SA}
```

Allow ${K8S_SERVICEACCOUNT} in ${K8S_NAMESPACE} to act as the new GCP service account

```
export K8S_SERVICEACCOUNT=workload-1

gcloud iam service-accounts add-iam-policy-binding ${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com \
--role roles/iam.workloadIdentityUser \
--member "serviceAccount:${GCP_PROJECT}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SERVICEACCOUNT}]"
```

On Kubernetes, create SA add annotation:

```
# create k8s service account
kubectl -n ${K8S_NAMESPACE} create serviceaccount ${K8S_SERVICEACCOUNT}

# annotate GKE's service account to link to the GCP identity
kubectl annotate serviceaccount ${K8S_SERVICEACCOUNT} \
--namespace ${K8S_NAMESPACE} \
iam.gke.io/gcp-service-account=${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com
```

Grant the new GCP service account permission to access the secret(s)

> ["literal-token-user","literal-token-password"]

```
gcloud secrets add-iam-policy-binding literal-token-user \
--member=serviceAccount:${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com \
--role=roles/secretmanager.secretAccessor

gcloud secrets add-iam-policy-binding literal-token-password \
--member=serviceAccount:${GCP_SERVICEACCOUNT}@${GCP_PROJECT}.iam.gserviceaccount.com \
--role=roles/secretmanager.secretAccessor
```

### Test the new deployments


```
# literal-token-user
# literal-token-password
```

Create the SecretProviderClass

```
cat <<EOF | kubectl apply -f -
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: workload-1-secrets
spec:
  provider: gcp
  parameters:
    secrets: |
      - resourceName: "projects/${GCP_PROJECT}/secrets/literal-token-user/versions/latest"
        path: "user"
      - resourceName: "projects/${GCP_PROJECT}/secrets/literal-token-password/versions/latest"
        path: "password"
EOF
```
Create deployments

```
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: secret-demo-2
spec:
  serviceAccountName: ${K8S_SERVICEACCOUNT}
  containers:
  - name: busybox
    image: busybox:1.34
    args:
    - sleep
    - "1000000"
    volumeMounts:
    - name: secret-data
      mountPath: "/etc/secrets"
      readOnly: true
  volumes:
  - name: secret-data
    csi:
      driver: secrets-store.csi.k8s.io
      readOnly: true
      volumeAttributes:
        secretProviderClass: "workload-1-secrets"
EOF
```

```
# limited of what we care about:
  serviceAccountName: ${K8S_SERVICEACCOUNT}
    volumeMounts:
    - name: secret-data
      mountPath: "/etc/secrets"
      readOnly: true
  volumes:
  - name: secret-data
    csi:
      driver: secrets-store.csi.k8s.io
      readOnly: true
      volumeAttributes:
        secretProviderClass: "workload-1-secrets"
```

Validate the deployment / secrets exist:

```
# exec into the pod
kubectl exec -it secret-demo-2 -- sh

# list the secrets
ls -lh /etc/secrets
```

Output looks like:

```
ls -lah /etc/secrets
total 0      
lrwxrwxrwx    1 root     root          15 Nov  8 17:19 password -> ..data/password
lrwxrwxrwx    1 root     root          11 Nov  8 17:19 user -> ..data/user
```

Lastly, check the file contents:

```
# should return "admin"
cat /etc/secrets/user
```

### Demo clean up

TODO: remove IAM bindings / annotations / RBAC in places not needed

```
kubectl delete job migrate-secrets
kubectl delete pod secret-demo
kubectl delete pod secret-demo-2
```
