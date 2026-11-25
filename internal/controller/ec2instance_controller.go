/*
Copyright 2026.

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






//                      USER / APPLICATION
//                              |
//                              | kubectl apply -f instance.yaml
//                              | kubectl edit / delete
//                              v
//               +-------------------------------+
//               |     KUBERNETES API SERVER     |
//               |                               |
//               | - Authentication              |
//               | - Authorization               |
//               | - Validation                  |
//               +---------------+---------------+
//                               |
//                 +-------------+-------------+
//                 |                           |
//                 | Store object              | LIST / WATCH
//                 v                           | notifications
//       +---------------------+               |
//       |        etcd         |               |
//       |                     |               |
//       | metadata            |               |
//       | spec: desired state |               |
//       | status: last        |               |
//       | observed state      |               |
//       +---------------------+               |
//                                             v
// +------------------------------------------------------------------+
// | YOUR OPERATOR — started by controller-runtime Manager             |
// |                                                                  |
// |                     +-----------------+                          |
// |                     |    INFORMER     |                          |
// |                     +--------+--------+                          |
// |                              |                                   |
// |              +---------------+---------------+                   |
// |              |                               |                   |
// |              v                               v                   |
// |    +-------------------+          +----------------------+       |
// |    | LOCAL OBJECT CACHE|          |    EVENT HANDLERS    |       |
// |    |                   |          |                      |       |
// |    | Copies of watched |          | Add / Update / Delete|       |
// |    | EC2Instance CRs   |          +-----------+----------+       |
// |    +-------------------+                      |                  |
// |              |                                v                  |
// |              |                    +----------------------+       |
// |              |                    |     ctrl.Request     |       |
// |              |                    |                      |       |
// |              |                    | namespace: default   |       |
// |              |                    | name: web-server-1   |       |
// |              |                    |                      |       |
// |              |                    | Identifies the object|       |
// |              |                    +-----------+----------+       |
// |              |                                |                  |
// |              |                                v                  |
// |              |                    +----------------------+       |
// |              |                    |      WORKQUEUE       |       |
// |              |                    | Pending requests     |       |
// |              |                    | Duplicates combined  |       |
// |              |                    +-----------+----------+       |
// |              |                                |                  |
// |              |                                v                  |
// |              |                    +----------------------+       |
// |              |                    |  CONTROLLER WORKER   |       |
// |              |                    | Takes a request      |       |
// |              |                    +-----------+----------+       |
// |              |                                |                  |
// |              |                                v                  |
// |              |                    +----------------------+       |
// |              |                    | Reconcile(ctx, req)  |       |
// |              |                    +-----------+----------+       |
// |              |                                |                  |
// |              | Normally supplies              v                  |
// |              +------------------> r.Get(ctx, req.NamespacedName,  |
// |                                        ec2Instance)              |
// +------------------------------------------------------------------+
//                                             |
//                                             v
//                               +---------------------------+
//                               | Was the CR found?         |
//                               +-------------+-------------+
//                                             |
//                      +----------------------+------------------+
//                      |                                         |
//                    NO: NotFound                              YES
//                      |                                         |
//                      v                                         v
//               Finish this run                    Inspect deletionTimestamp
//                                                                |
//                                   +----------------------------+------+
//                                   |                                   |
//                             DELETION SET                         NOT DELETING
//                                   |                                   |
//                                   v                                   v
//                     Read status.instanceID              Ensure finalizer exists
//                                   |                                   |
//                                   v                                   v
//                     Ask AWS to terminate VM             Save with r.Update(...)
//                                   |                                   |
//                                   v                                   v
//                     Finish required cleanup             Check status.instanceID
//                                   |                                   |
//                                   v                       +-----------+----------+
//                     Remove OUR finalizer                  |                      |
//                                   |                     EMPTY                PRESENT
//                                   v                       |                      |
//                     Save with r.Update(...)               v                      v
//                                   |               AWS RunInstances       AWS DescribeInstances
//                                   v                       |                      |
//                     Kubernetes removes CR                 v                      v
//                     when ALL finalizers           Get instance details   Inspect actual state
//                     have been removed                     |                      |
//                                                           |           +----------+----------+
//                                                           |           |                     |
//                                                           |         EXISTS             CONFIRMED
//                                                           |           |                 MISSING
//                                                           |           |                     |
//                                                           v           v                     v
//                                                     Copy observed details           Clear old status
//                                                     into ec2Instance.Status                 |
//                                                               |                             v
//                                                               v                     Save cleared status
//                                                     r.Status().Update(...)                  |
//                                                               |                             v
//                                                               |                     Request another run
//                                                               |                             |
//                                                               |                             v
//                                                               |                     Next run can create
//                                                               |                     a replacement VM
//                                                               v
//                                                     Kubernetes API server
//                                                               |
//                                                               v
//                                                     Persist status in etcd
//                                                               |
//                                                               v
//                                                     Watch notification
//                                                               |
//                                                               v
//                                                     Informer -> Handler
//                                                               |
//                                                               v
//                                                          Workqueue


// RECONCILE RETURN VALUES
// ======================

// return ctrl.Result{}, nil
//     |
//     +--> Finish this run; no new retry requested.
//          Another event or already scheduled request can still run it again.

// return ctrl.Result{}, err
//     |
//     +--> Normal error: automatically retry with backoff.

// return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
//     |
//     +--> Schedule another check after 30 seconds.
//          Useful for checking AWS changes that Kubernetes cannot watch directly.


// KEY DISTINCTION
// ===============

// Kubernetes CR change -> Watch -> Queue -> Reconcile

// AWS VM change        -> Needs polling or a separate event integration
//                         -> Queue -> Reconcile




package controller

import (
	"context"
	"time"
    "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"github.com/aws/aws-sdk-go-v2/aws"
	"sigs.k8s.io/controller-runtime/pkg/client"
	log "sigs.k8s.io/controller-runtime/pkg/log"

	computev1 "github.com/kunalpandey1/custom-kubernetes-operator/api/v1"
)

// EC2InstanceReconciler reconciles a EC2Instance object
// reconciler logic goes here. It is the main logic of your operator, and it is where you will write the code to create, update, and delete AWS EC2 instances based on the desired state specified in the EC2Instance CRD.
type EC2InstanceReconciler struct {
	client.Client                 // it gives you actual kubernetes client to talk to the API server or k8s cluster, and lets you get, list, create, update, delete resources.
	Scheme        *runtime.Scheme // this is used to convert between Go structs and the YAML/JSON representation of the resources. It is used to register your types with the scheme, so that the controller can recognize them.
}

// custom markers for rbac

// +kubebuilder:rbac:groups=compute.cloud.com,resources=ec2instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.cloud.com,resources=ec2instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.cloud.com,resources=ec2instances/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.


// After updating the status of the resource (e.g., with r.Status().Update), the Kubernetes API server
// will emit an update event for the resource. This event will be picked up by the controller-runtime
// and will cause the Reconcile function to be called again for the same resource. This is why, after
// updating the status, the reconciler is called again: it is a result of the Kubernetes watch mechanism
// and ensures that the controller can observe and react to any changes, including those it made itself.
// This pattern is common in Kubernetes controllers to ensure eventual consistency and to handle
// situations where the status update may not have been fully applied or observed yet.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
// if you return only error the result will be ignored and it goes into exponential backoff it will retry so this is where self healing works in k8s
func (r *EC2InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx) // Why ctx goes into logf.FromContext(ctx): the logger is inside the backpack, so you hand over the backpack to take the logger out. Without ctx, that function has nowhere to find the logger.


	l.Info("=== RECONCILE LOOP STARTED ===", "namespace", req.Namespace, "name", req.Name)
	// Create a new instance of the Ec2Instance struct to hold the data retrieved from the Kubernetes API.
	// This struct will be populated with the current state of the Ec2Instance resource specified by the request.
	ec2Instance := &computev1.EC2Instance{}
    // Retrieve the Ec2Instance resource from the Kubernetes API server using the provided request's NamespacedName.
	if err := r.Get(ctx, req.NamespacedName, ec2Instance); err!=nil { 	 // r.Get returns only an error as its return value, and the object's data comes out through a side effect, because it writes that data into the variable you passed in.
		if errors.IsNotFound(err){
			l.Info("Instance Deleted. No need to reconcile")
			// Kubernetes will not retry - done, wait for next event
			return ctrl.Result{}, nil
		}
		// Kubernetes will retry with backoff
		return ctrl.Result{}, err
	}

	// ec2Instance := &computev1.EC2Instance{}
	// r.Get(ctx, req.NamespacedName, ec2Instance)
	// l.Info("Reconciling EC2Instance", "name", ec2Instance.Name)
	// fmt.Println("I got a request for an ec2 instance in the namespace", req.NamespacedName)
	// fmt.Println("The ec2 instance name is", ec2Instance.Spec.InstanceType)
	// fmt.Println("Instance Type is", ec2Instance.Spec.InstanceType)
	// fmt.Println("Ami ID is", ec2Instance.Spec.AMIId)
	// fmt.Println("Subnet is", ec2Instance.Spec.Subnet)
	// fmt.Println("Tags are ", ec2Instance.Spec.Tags)  
	// fmt.Println("Additional Storage is ", ec2Instance.Spec.Storage.AdditionalVolumes)
	// l.Info("Reconciled EC2Instance", "name", ec2Instance.Name) // metadata.name The name of the Kubernetes object, e.g. my-server


	// DeletionTimestamp tells the controller that someone requested deletion of this Kubernetes object.
	// eg:- kubectl delete ec2instance my-instance





		 // Checking idempotency and drift detection 

		// Kubernetes status says instance i-123 exists
		// 		↓
		// Controller asks AWS: does i-123 actually still exist, and what is its state?
		// 			↓
		// Compare AWS reality with Kubernetes status
		// 			↓
		// Update status if they differ


		// If an EC2 instance was already created, verify that it still exists in AWS.
if ec2Instance.Status.InstanceID != "" {
	l.Info("Checking existing EC2 instance", "instanceID", ec2Instance.Status.InstanceID)

	instanceExists, instance, err := checkEC2InstanceExists(
		ctx,
		ec2Instance.Status.InstanceID,
		ec2Instance,
	)
	if err != nil {
		l.Error(err, "Failed to check EC2 instance", "instanceID", ec2Instance.Status.InstanceID)
		return ctrl.Result{}, err
	}

	// The instance was deleted outside Kubernetes.
	// Clear status, requeue, and let the next reconcile create a replacement.
	if !instanceExists {
		l.Info("EC2 instance no longer exists; preparing to recreate it",
			"instanceID", ec2Instance.Status.InstanceID)

		ec2Instance.Status.InstanceID = ""
		ec2Instance.Status.State = ""
		ec2Instance.Status.PublicIP = ""
		ec2Instance.Status.PrivateIP = ""
		ec2Instance.Status.PublicDNS = ""
		ec2Instance.Status.PrivateDNS = ""

		if err := r.Status().Update(ctx, ec2Instance); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	}

	// The instance exists: synchronize Kubernetes status with AWS's current state.
	ec2Instance.Status.State = string(instance.State.Name)
	ec2Instance.Status.PublicIP = aws.ToString(instance.PublicIpAddress)
	ec2Instance.Status.PrivateIP = aws.ToString(instance.PrivateIpAddress)
	ec2Instance.Status.PublicDNS = aws.ToString(instance.PublicDnsName)
	ec2Instance.Status.PrivateDNS = aws.ToString(instance.PrivateDnsName)

	if err := r.Status().Update(ctx, ec2Instance); err != nil {
		return ctrl.Result{}, err
	}

	l.Info("EC2 instance still exists; status synchronized",
		"instanceID", ec2Instance.Status.InstanceID,
		"state", ec2Instance.Status.State)

	return ctrl.Result{}, nil
}



	l.Info("Creating new instance")

	// see more in detail about finalizer in below comments at last

	l.Info("=== ABOUT TO ADD FINALIZER ===")
	ec2Instance.Finalizers = append(ec2Instance.Finalizers, "ec2instance.compute.cloud.com")		// ec2Instance.Finalizers is the list at metadata.finalizers. It's reachable directly because ObjectMeta is embedded in the struct. and append(list, item) is Go's way of adding an item to the end of a list. So this adds the string "ec2instance.compute.cloud.com" to the list.
	if err := r.Update(ctx, ec2Instance); err != nil { // r.Update() WILL trigger the Reconcile function again via Kubernetes watch mechanism
		l.Error(err, "Failed to add finalizer")
		// Kubernetes will retry with backoff
		return ctrl.Result{
			Requeue: true,
		}, err
	}
	l.Info("=== FINALIZER ADDED - This update will trigger a NEW reconcile loop, but current reconcile continues ===")

// Create a new instance
	l.Info("=== CONTINUING WITH EC2 INSTANCE CREATION IN CURRENT RECONCILE ===")

	// This block is the act step of your reconcile loop. It calls the AWS 
	// function we just went through, then copies the result into the object's .Status

	createdInstanceInfo, err := createEc2Instance(ec2Instance)
	if err != nil {
		l.Error(err, "Failed to create EC2 instance")
		// Kubernetes will retry with backoff
		return ctrl.Result{}, err
	}

	l.Info("=== ABOUT TO UPDATE STATUS - This will trigger reconcile loop again ===",
		"instanceID", createdInstanceInfo.InstanceID,
		"state", createdInstanceInfo.State)


		// copies the actual EC2 details returned by AWS into your Kubernetes custom resource’s status.
		// object.status is the part of a Kubernetes object that stores its current observed state.
		// - spec = what the user wants
		// - status = what is actually happening right now

		ec2Instance.Status.InstanceID = createdInstanceInfo.InstanceID
		ec2Instance.Status.State = createdInstanceInfo.State
		ec2Instance.Status.PublicIP = createdInstanceInfo.PublicIP
		ec2Instance.Status.PrivateIP = createdInstanceInfo.PrivateIP
		ec2Instance.Status.PublicDNS = createdInstanceInfo.PublicDNS
		ec2Instance.Status.PrivateDNS = createdInstanceInfo.PrivateDNS


		// The Reconcile function must return a ctrl.Result and an error.
		// Returning ctrl.Result{} with nil error means the reconciliation was successful
		// and no requeue is requested. If an error is returned, the controller will
		// automatically requeue the request for another attempt.
		// Sends Requeue ( bool ) and RequeueAfter ( time.Duration ).
		//return ctrl.Result{}, nil


		// imp
		// Important: these assignments only change the object in memory. To save the status back to Kubernetes, the controller must then call:

	err = r.Status().Update(ctx, ec2Instance)
	if err != nil {
		l.Error(err, "Failed to update status")
		// Kubernetes will retry with backoff
		return ctrl.Result{}, err
	}
	l.Info("=== STATUS UPDATED - Reconcile loop will be triggered again ===")

	//fmt.Println("Instance id at the end of the reconcile is", ec2Instance.Status.InstanceID)

	// Kubernetes will not retry - done, wait for next event
	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}


// SetupWithManager sets up the controller with the Manager.
// SetupWithManager registers the Ec2InstanceReconciler with the controller manager.
// It configures the controller to watch for changes to Ec2Instance resources.
// The controller will be named "ec2instance" for logging and metrics purposes.
// The Complete(r) call finalizes the setup, associating the reconciler logic with this controller.
// SetupWithManager sets up the controller with the Manager.
// this function is called by the main function to register the controller with the manager, so that it can start watching for changes to EC2Instance resources and reconcile them.
func (r *EC2InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&computev1.EC2Instance{}).
		Named("ec2instance").
		Complete(r)
}

// nil means "nothing". In Go, nil is the empty value for things like errors,
// pointers and maps. When a function returns an error and the value is nil,
// there is no error, so it succeeded.

// err := r.Get(ctx, key, obj)
// if err != nil {
//     // an error happened
// }
// err == nil means it worked

// You return								Meaning

// ctrl.Result{}, nil						Done. Don't run again until the next event.
// ctrl.Result
// {RequeueAfter: 60 * time.Second}, nil	Done, but run again in 60 seconds (use this to keep re-checking AWS).
// ctrl.Result{}, err						It failed. The controller retries with increasing delay (the self-healing path from your sad-path diagram). The Result is ignored when an error is returned.

// ctrl.Request (the input to Reconcile)

// A struct that holds exactly two strings: Namespace and Name. It identifies which object to reconcile. It carries no other data.

// ctrl.Result (the output from Reconcile)

// A struct that tells the controller what to do after your function returns. It has two fields: RequeueAfter (run this object again after a given time) and Requeue (run it again right away, which is the older way of asking for this). The empty ctrl.Result{} has both unset, so it means "don't run again until something changes".

// Types in ctrl
// =============

// +--------------+--------------------------------------------------------------------------------------+--------------------------------------------------------------+
// | Name         | What it is                                                                           | Where you've seen it                                         |
// +--------------+--------------------------------------------------------------------------------------+--------------------------------------------------------------+
// | ctrl.Manager | The manager itself. It runs the cache, client, controllers, webhooks, and metrics.  | SetupWithManager(mgr ctrl.Manager) in your controller file   |
// | ctrl.Options | Settings passed when creating the manager: scheme, metrics, leader election, etc.   | ctrl.Options{...} in main.go                                 |
// | ctrl.Request | Namespace and name of the object to reconcile.                                      | Reconcile(ctx, req ctrl.Request)                             |
// | ctrl.Result  | What the controller should do after Reconcile() returns.                            | return ctrl.Result{}, nil                                    |
// | ctrl.Builder | Returned by NewControllerManagedBy; chain .For(), .Owns(), and .Complete(r).        | SetupWithManager                                             |
// +--------------+--------------------------------------------------------------------------------------+--------------------------------------------------------------+

// Functions and variables in ctrl
// ================================

// +-------------------------------------------------+--------------------------------------------------------------------------------------------------+
// | Name                                            | What it does                                                                                     |
// +-------------------------------------------------+--------------------------------------------------------------------------------------------------+
// | ctrl.NewManager(config, options)                | Creates the manager.                                                                             |
// | ctrl.GetConfigOrDie() / ctrl.GetConfig()        | Finds cluster connection details from kubeconfig or in-cluster config.                           |
// |                                                 | GetConfigOrDie exits if it fails.                                                                |
// | ctrl.SetupSignalHandler()                       | Lets the manager shut down cleanly on Ctrl+C or Pod termination.                                 |
// | ctrl.NewControllerManagedBy(mgr)                | Starts building a controller.                                                                    |
// | ctrl.NewWebhookManagedBy(mgr)                   | Starts building a webhook.                                                                       |
// | ctrl.SetLogger(...) / ctrl.Log                  | Sets the logger and accesses the default logger.                                                 |
// | ctrl.LoggerFrom(ctx)                            | Gets the logger from a context; similar to logf.FromContext(ctx).                                |
// | ctrl.SetControllerReference(owner, obj, scheme) | Sets an owner reference so created resources are deleted with their parent.                      |
// | ctrl.CreateOrUpdate(...)                        | Creates an object if absent; otherwise updates it.                                               |
// +-------------------------------------------------+--------------------------------------------------------------------------------------------------+

// ctx :-

// The problem

// Your Reconcile() calls other functions, and those call others:

// Reconcile()
//    └── r.Get()          → talks to the API server (takes time)
//    └── RunInstances()   → talks to AWS (takes time)

// Now suppose you press Ctrl+C while Reconcile() is halfway through. The operator wants to stop, but r.Get() or the AWS call may be in the middle of waiting for a reply. How do those functions find out that they should stop?

// The solution: pass one shared object to all of them

// A ctx is a small object that you hand to each function. All of those functions hold the same ctx, so they can all look at it.

// Inside it there is a "should we stop?" signal:

// Normally, the signal says "keep going".
// When the operator is shutting down, the signal changes to "stop".
// Every function that was given ctx sees the change and stops what it's doing.

// That's the main job of ctx.

// go
// func Reconcile(ctx context.Context, ...) {
//     r.Get(ctx, ...)              // gets the ctx, so it can stop if told to
//     r.EC2.RunInstances(ctx, ...) // gets the ctx, so it can stop if told to
// }

// That's why you see ctx as the first thing in so many function calls. It is being passed along so the signal reaches everyone.

// The second job: carrying extra things

// A ctx can also hold extra items that were put into it earlier. controller-runtime puts a logger into it before calling your Reconcile().

// So here:

// go
// l := logf.FromContext(ctx)

// means: "look inside ctx and give me the logger that was stored there." That's the only reason ctx is passed to that function. The logger lives inside it, so the function needs ctx to find it.

// What you actually do with it

// Question									Answer
// Do I create ctx?							No. controller-runtime creates it and gives it to your Reconcile().
// What do I do with it?						Pass it along to any function that asks for a ctx.
// Do I ever look inside it myself?			Only through helpers like logf.FromContext(ctx).

// what is r *EC2InstanceReconciler ? and what it holds and what can we do with it ?

// What r *EC2InstanceReconciler is

// It's the object your Reconcile() function belongs to. In Go, a function written like this is a method of a type:

// go
// func (r *EC2InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
// //    ^^^^^^^^^^^^^^^^^^^^^^^^^ the object this function belongs to
// EC2InstanceReconciler is the type: a struct defined in ec2instance_controller.go.
// r is the variable name for that object inside the function (like this or self in other languages).
// The * means r is a pointer, so it points to the one real reconciler object instead of a copy. This is the same object you created in main.go with &controller.EC2InstanceReconciler{...}.
// What it holds

// Open your ec2instance_controller.go and you'll see the struct, which looks like this:

// type EC2InstanceReconciler struct {
//     client.Client
//     Scheme *runtime.Scheme
// }

// It holds two things, and main.go fills in both:

// &controller.EC2InstanceReconciler{
//     Client: mgr.GetClient(),   // the client
//     Scheme: mgr.GetScheme(),   // the scheme
// }
// Field									What it is
// client.Client							The client, used to talk to the Kubernetes API server (read, create, update, delete objects). It is embedded, so its methods become r.Get, r.Update and so on.
// Scheme									The scheme, the list of types that your operator understands.

// What you can do with it

// Because the client is embedded, these methods are available directly on r:

// Call							What it does

// r.Get(ctx, key, obj)			Fetch one object (you used this)
// r.List(ctx, list)				Fetch many objects
// r.Create(ctx, obj)				Create an object in the cluster
// r.Update(ctx, obj)				Update an object
// r.Delete(ctx, obj)				Delete an object
// r.Status().Update(ctx, obj)		Update only the status part of an object
// r.Scheme						Access the scheme, used for things like setting owner references

// These all talk to the Kubernetes API server. Reads go through the manager's cache,
// and writes go to the API server directly.

// You can add your own fields

// The struct is yours, so you can put anything the reconciler needs inside it. For your operator, you'd add the AWS client:

// type EC2InstanceReconciler struct {
//     client.Client
//     Scheme *runtime.Scheme
//     EC2    *ec2.Client      // AWS client, so Reconcile can call r.EC2.RunInstances(...)
// }

// Then you set it in main.go when creating the reconciler. That is how the earlier skeleton I wrote could call r.EC2.RunInstances(...).

// Why this design

// Reconcile() is called by the manager, not by you, and it only receives ctx and req. It has no other way to reach the Kubernetes client, the AWS client or any settings. So everything it needs has to be stored inside r beforehand, and r is how the function reaches those tools.

// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | Method                        | Takes                                               | What you get                                                 |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Get(ctx, key, obj)          | key: namespace/name.                                | obj is filled with the object from the cluster.              |
// |                               | obj: empty object of the correct type.              |                                                              |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.List(ctx, list, opts...)    | list: empty list object, e.g.                       | list.Items is filled with all matching objects.              |
// |                               | &computev1.EC2InstanceList{}.                       |                                                              |
// |                               | opts: optional filters.                             |                                                              |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Create(ctx, obj)            | obj: a new object with its name and spec set.       | Object is saved in the cluster. obj receives server-added    |
// |                               |                                                     | values such as UID, resourceVersion, and creation time.      |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Update(ctx, obj)            | obj: the complete object with your changes.         | Cluster object is replaced. obj gets a new resourceVersion.  |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Delete(ctx, obj)            | obj: only needs its name and namespace.             | Object is deleted from the cluster.                          |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Patch(ctx, obj, patch)      | obj plus a patch describing only the change.        | Only patched fields change; safer than a full Update.         |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+
// | r.Status().Update(ctx, obj)   | obj with new Status values.                         | Only status is saved. Spec changes in obj are ignored.       |
// +-------------------------------+-----------------------------------------------------+--------------------------------------------------------------+

// Note:
//   ctx is always the first argument. It carries the stop/cancellation signal.

// Examples

// Get: fetch one object:

// inst := &computev1.EC2Instance{}                     // empty
// err := r.Get(ctx, req.NamespacedName, inst)          // inst is now filled in

// List: fetch many, optionally filtered:

// list := &computev1.EC2InstanceList{}
// err := r.List(ctx, list, client.InNamespace("default"))
// for _, item := range list.Items {                    // results are in list.Items
//     fmt.Println(item.Name)
// }

// Create: make a new object in the cluster (for example, a ConfigMap your controller manages):

// cm := &corev1.ConfigMap{
//     ObjectMeta: metav1.ObjectMeta{Name: "my-cm", Namespace: "default"},
//     Data:       map[string]string{"key": "value"},
// }
// err := r.Create(ctx, cm)

// Update: change an object you already fetched:

// inst.Spec.InstanceType = "t3.large"
// err := r.Update(ctx, inst)

// Status update: record what the controller found (this is how instanceID and publicIP get saved):

// inst.Status.InstanceID = "i-0abc123"
// inst.Status.Phase = "Running"
// err := r.Status().Update(ctx, inst)

// Delete:

// err := r.Delete(ctx, inst)










// Scenario 1: the object exists

// You ran kubectl apply -f instance.yaml, and the operator receives the event.

// Step  What happens                                      Values
// ----  ------------------------------------------------  ------------------------------------------------------
// 1     Reconcile is called                               req.Namespace = "default", req.Name = "my-ec2-instance"
// 2     Empty object is created                           ec2Instance = {} (all fields empty)
// 3     r.Get(...) runs. The object exists, so it fills  ec2Instance.Spec.InstanceType = "t3.micro",
//       ec2Instance.                                    ec2Instance.Name = "my-ec2-instance"
// 4     Get returns                                      err = nil
// 5     Check err != nil                                 nil != nil is false
// 6     The block is skipped                             no return happens
// 7     Execution continues below the block              your compare and act logic runs, using the filled ec2Instance

// The IsNotFound line is never even reached.


// Scenario 2: the object was deleted

// You ran kubectl delete ec2instance my-ec2-instance. The delete triggers a
// Reconcile, but by the time it runs, the object is gone.

// Step  What happens                                      Values
// ----  ------------------------------------------------  ------------------------------------------------------
// 1     Reconcile is called                               req.Namespace = "default", req.Name = "my-ec2-instance"
// 2     Empty object is created                           ec2Instance = {}
// 3     r.Get(...) runs. The API server answers 404.     ec2Instance stays empty
// 4     Get returns                                      err = ec2instances.compute.cloud.com
//                                                        "my-ec2-instance" not found
//                                                        (Reason: NotFound, Code: 404)
// 5     Check err != nil                                 true, so the block runs
// 6     Check errors.IsNotFound(err)                     The reason inside err is NotFound, so true
// 7     Log                                              Instance Deleted. No need to reconcile
// 8     return ctrl.Result{}, nil                        Reconcile ends here
// 9     Controller sees no error and no requeue flags    Level 4: it stops and waits for the next event

// Nothing below the block runs. That's good, because there's no data to work with.


// Scenario 3: the API server can't be reached

// The cluster is down or the network drops, so the request itself fails.

// Step  What happens                                      Values
// ----  ------------------------------------------------  ------------------------------------------------------
// 1     Reconcile is called                               req.Name = "my-ec2-instance"
// 2     Empty object is created                           ec2Instance = {}
// 3     r.Get(...) runs but can't connect                ec2Instance stays empty
// 4     Get returns                                      err = dial tcp 127.0.0.1:6443: connection refused
// 5     Check err != nil                                 true, so the block runs
// 6     Check errors.IsNotFound(err)                     This error has no NotFound reason, so false.
//                                                        The inner block is skipped.
// 7     return ctrl.Result{}, err                        Reconcile ends here, returning the error
// 8     Controller sees a non-nil error                  Level 1: it retries with exponential backoff

// When the API server is back, a later retry will find the object and go through
// Scenario 1.






// Finalizers


// The problem it solves

// When you run kubectl delete, Kubernetes removes the object from the cluster immediately. If your operator created something outside Kubernetes for that object, like a real EC2 instance in AWS, nothing deletes it. You'd be left with a running, billing AWS instance and no Kubernetes object pointing to it.

// Also, as we saw earlier, once the object is gone, r.Get() returns "not found", and you can no longer read Status.InstanceID to know which AWS instance to clean up.

// What a finalizer is

// A finalizer is just a string in the object's metadata.finalizers list. It means: "do not fully delete this object until I have finished my cleanup."


// metadata:
//   name: my-ec2-instance
//   finalizers:
//     - compute.cloud.com/ec2-cleanup

// While that string is in the list, Kubernetes refuses to remove the object.

// What happens on delete
// 1. kubectl delete ec2instance my-ec2-instance
// 2. The object is NOT removed. Kubernetes sets metadata.deletionTimestamp on it.
//    (the object is now "terminating", but still readable)
// 3. Your Reconcile() runs and sees deletionTimestamp is set
// 4. Your code does the cleanup (terminates the AWS instance)
// 5. Your code removes its finalizer from the list
// 6. The list is now empty, so Kubernetes deletes the object for real










// Finalizer :- 



// A finalizer is a string stored in a Kubernetes object’s metadata that tells Kubernetes:
// “Do not permanently delete this object until the controller finishes cleanup.”

// In your code, the finalizer is:
// "ec2instance.compute.cloud.com"

// When this code runs:
// ec2Instance.Finalizers = append(
//     ec2Instance.Finalizers,
//     "ec2instance.compute.cloud.com",
// )

// r.Update(ctx, ec2Instance)

// Kubernetes saves this inside your custom resource:
// metadata:
//   finalizers:
//     - ec2instance.compute.cloud.com

// Why you need it here
// Your Kubernetes object creates something outside Kubernetes: an AWS EC2 instance.
// EC2Instance CR in Kubernetes
//         ↓
// Controller creates
//         ↓
// EC2 VM in AWS

// Later, when you run:
// kubectl delete ec2instance web-server-1

// you want the matching AWS VM to be deleted too.

// Without a finalizer, Kubernetes can immediately delete the EC2Instance object:
// kubectl delete EC2Instance
//         ↓
// Kubernetes deletes the CR immediately
//         ↓
// Controller loses status.instanceID
//         ↓
// AWS EC2 VM may remain running
//         ↓
// Orphaned VM and possible AWS charges

// The finalizer prevents that.
// What actually happens with a finalizer
// 1. Controller adds finalizer to the EC2Instance CR
//         ↓
// 2. Controller creates the EC2 VM
//         ↓
// 3. User runs: kubectl delete ec2instance web-server-1
//         ↓
// 4. Kubernetes sets metadata.deletionTimestamp
//    but does NOT remove the CR yet
//         ↓
// 5. Controller sees deletionTimestamp
//         ↓
// 6. Controller reads Status.InstanceID
//    and deletes that EC2 VM from AWS
//         ↓
// 7. Controller removes the finalizer
//         ↓
// 8. Kubernetes sees no finalizers remain
//         ↓
// 9. Kubernetes permanently deletes the EC2Instance CR

// The key point is: while deletion is in progress, the CR is still available. Your controller can still access:
// ec2Instance.Status.InstanceID

// and use it to delete the correct AWS instance.