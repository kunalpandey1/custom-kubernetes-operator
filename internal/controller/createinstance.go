package controller

import (
	"context" // context is Go’s built-in package for carrying request-related information such as cancellation signals, timeouts, and deadlines.
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	computev1 "github.com/kunalpandey1/custom-kubernetes-operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)



func createEc2Instance(ec2Instance *computev1.EC2Instance)(createdInstanceInfo *computev1.CreateInstanceInfo, err error){
   l := log.Log.WithName("createEc2Instance") 	// log.Log.WithName(...) makes a logger labelled createEc2Instance. Since there's no ctx here, it uses the global logger instead of the labelled one from ctx, so these lines won't carry the object name or reconcile ID.

   l.Info("=== STARTING EC2 INSTANCE CREATION ===",
   "ami", ec2Instance.Spec.AMIId,
   "instanceType", ec2Instance.Spec.InstanceType,
	"region", ec2Instance.Spec.Region)

	// create the client for ec2 instance
	ec2Client := awsClient(ec2Instance.Spec.Region)

	// create the input value struct for the run instances
	runInput := &ec2.RunInstancesInput{ // & takes the address of that value, giving you a pointer to it, of type *ec2.RunInstancesInput.
		ImageId:      aws.String(ec2Instance.Spec.AMIId),
		InstanceType: ec2types.InstanceType(ec2Instance.Spec.InstanceType),
		KeyName:      aws.String(ec2Instance.Spec.KeyPair),
		SubnetId:     aws.String(ec2Instance.Spec.Subnet),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		//SecurityGroupIds: []string{ec2Instance.Spec.SecurityGroups[0]},
		// aws.String and aws.Int32 convert regular values into pointers, 
		// because the AWS SDK uses pointer fields to distinguish between a 
		// value not being provided (nil) and a value being explicitly provided.
	}

	l.Info("=== CALLING AWS RunInstances API ===")

	// call AWS to create the instance

	result, err := ec2Client.RunInstances(context.TODO(), runInput)
	if err != nil {
		l.Error(err, "Failed to create EC2 instance")
		return nil, fmt.Errorf("failed to create EC2 instance: %w", err) // On failure it logs and returns nil plus a new error. %w wraps the original error inside the new message, so the cause isn't lost. Your Reconcile() then returns this error, and the controller retries with backoff.
	}

	// sanity check
		if len(result.Instances) == 0 {
		l.Error(nil, "No instances returned in RunInstancesOutput")
		fmt.Println("No instances returned in RunInstancesOutput")
		return nil, nil // If AWS returned no instances, something is wrong. This returns nil, nil, meaning "no info and no error". The caller will likely read fields from a nil result and crash with a nil pointer panic. This should return a real error instead.
	}

	// Till here, the instance is created and we have
	// Instance ID, private dns and IP, instance type and image id.
	// take the first instance
	inst := result.Instances[0]
	l.Info("=== EC2 INSTANCE CREATED SUCCESSFULLY ===", "instanceID", *inst.InstanceId) // dereferencing it to get the value here 

	// Now we need to wait for the instance to be running and then get the public ip and dns
	l.Info("=== WAITING FOR INSTANCE TO BE RUNNING ===")


	// wait until it's running
    // A waiter is an SDK helper that polls AWS every few seconds until the 
	// instance reaches the running state, or gives up after 3 minutes. 
	// The reason for waiting: public IPs and DNS names aren't assigned 
	// until the instance is running.

	// The trade-off is that this blocks Reconcile() for up to 3 minutes. 
	// A more operator-friendly way is to save the instance ID, 
	// return RequeueAfter: 10 * time.Second, and check the state on 
	// the next reconcile, so the worker isn't stuck.

	runWaiter := ec2.NewInstanceRunningWaiter(ec2Client)
	maxWaitTime := 3 * time.Minute

    err = runWaiter.Wait(context.TODO(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{*inst.InstanceId},
	}, maxWaitTime)

	if err != nil {
		l.Error(err, "Failed to wait for instance to be running")
		return nil, fmt.Errorf("failed to wait for instance to be running: %w", err)
	}


	// After creating the instance, we waited and now we describe to
	// 1. Get the public IP and dns as it takes some time for it
	// 2. Getting the state of the instance.
	// We do this so we can send the instance's state to the status of the custom resource. for user to see with kubectl get ec2instances
	l.Info("=== CALLING AWS DescribeInstances API TO GET INSTANCE DETAILS ===")
	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []string{*inst.InstanceId},
	}

	describeResult, err := ec2Client.DescribeInstances(context.TODO(), describeInput)
	if err != nil {
		l.Error(err, "Failed to describe EC2 instance")
		return nil, fmt.Errorf("failed to describe EC2 instance: %w", err)
	}

	fmt.Println("Describe result", "public ip", *describeResult.Reservations[0].Instances[0].PublicDnsName, "state", describeResult.Reservations[0].Instances[0].State.Name)

	// You get "invalid memory address or nil pointer dereference" here if any of the following are true:
	// - result.Instances is nil or has length 0
	// - Any of the pointer fields (e.g., PublicIpAddress, PrivateIpAddress, etc.) are nil

	// To avoid this, always check for nil and length before dereferencing:

	// Wait for a bit to allow instance fields to be populated

	fmt.Printf("Private IP of the instance: %v", derefString(inst.PrivateIpAddress))
	fmt.Printf("State of the instance: %v", describeResult.Reservations[0].Instances[0].State.Name)
	fmt.Printf("Private DNS of the instance: %v", derefString(inst.PrivateDnsName))
	fmt.Printf("Instance ID of the instance: %v", derefString(inst.InstanceId))
	fmt.Println("Instance Type of the instance: ", inst.InstanceType)
	fmt.Printf("Image ID of the instance: %v", derefString(inst.ImageId))
	fmt.Printf("Key Name of the instance: %v", derefString(inst.KeyName))



	// 	A reservation is AWS’s response-level grouping for EC2 instances.

	// 	When you call DescribeInstances, AWS returns:
	// DescribeInstancesOutput
	//   └── Reservations
	//        └── Instances

	// Each reservation can contain one or more EC2 instances that were launched together with the same request/settings.

	// block until the instance is running
	// blockUntilInstanceRunning(ctx, ec2Instance.Status.InstanceID, ec2Instance)

	// Get the instance details safely (public IP/DNS might be nil for private subnets)

	// This copies the values into a CreatedInstanceInfo struct and returns 
	// it with nil for the error. Reconcile() is meant to copy these into 
	// the object's .Status, which is what you see with kubectl get ec2instances.

	instance := describeResult.Reservations[0].Instances[0]
	createdInstanceInfo = &computev1.CreateInstanceInfo{
		InstanceID: *inst.InstanceId,
		State:      string(instance.State.Name),
		PublicIP:   derefString(instance.PublicIpAddress),
		PrivateIP:  derefString(instance.PrivateIpAddress),
		PublicDNS:  derefString(instance.PublicDnsName),
		PrivateDNS: derefString(instance.PrivateDnsName),
	}

	l.Info("=== EC2 INSTANCE CREATION COMPLETED ===",
		"instanceID", createdInstanceInfo.InstanceID,
		"state", createdInstanceInfo.State,
		"publicIP", createdInstanceInfo.PublicIP)


		// Optionally, update ec2Instance.Status.InstanceID = *result.Instances[0].InstanceId

	// For now, just return nil to indicate success.
	return createdInstanceInfo, nil

}


// derefString is a helper function to safely dereference *string
func derefString(s *string) string {
	if s != nil {
		return *s
	}
	return "<nil>"
}





// build request from Spec
//    ↓
// RunInstances            → instance created in AWS (still starting)
//    ↓
// Wait until running      → blocks up to 3 min
//    ↓
// DescribeInstances       → get IPs, DNS, state
//    ↓
// return CreatedInstanceInfo  → Reconcile() saves it into .Status