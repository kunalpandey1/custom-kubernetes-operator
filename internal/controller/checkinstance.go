package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	computev1 "github.com/kunalpandey1/custom-kubernetes-operator/api/v1"
)

func checkEC2InstanceExists(ctx context.Context, instanceID string, ec2Instance *computev1.EC2Instance) (bool, *ec2types.Instance, error) {
	// create the client for ec2 instance
	fmt.Println("Checking instance ", instanceID)
	ec2Client := awsClient(ec2Instance.Spec.Region)

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	}

	result, err := ec2Client.DescribeInstances(ctx, input)
	if err != nil {
		// Check if it's a "not found" error
		if strings.Contains(err.Error(), "InvalidInstanceID.NotFound") {
			return false, nil, nil
		}
		return false, nil, err
	}

	fmt.Println("Legnth of Reservations are ", len(result.Reservations))

	// AWS did not return an instance. Because your request filters for running, this means the instance either:
	// - does not exist, or
	// - exists but is not currently running.

	// Check if we got any instances back
	if len(result.Reservations) == 0 {
		// No reservations means the instance is not found or not running
		return false, nil, nil
	}
	// true                              // a running instance was found
	// &result.Reservations[0].Instances[0] // pointer to that EC2 instance's information
	// nil                               // no error
	return true, &result.Reservations[0].Instances[0], nil
}

// false // instance does not exist
// nil   // no instance data
// nil   // not a controller error; AWS successfully told us it is missing
