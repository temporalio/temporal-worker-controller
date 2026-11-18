package ststokenrefresher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type stsTokenIssuer struct {
	client   *sts.Client
	audience string
}

func (s stsTokenIssuer) Issue(ctx context.Context) (Token, error) {
	result, err := s.client.GetWebIdentityToken(ctx, &sts.GetWebIdentityTokenInput{
		Audience:         []string{s.audience},
		SigningAlgorithm: aws.String("RS256"),
		DurationSeconds:  aws.Int32(900),
	})
	if err != nil {
		return Token{}, err
	}
	if result == nil || result.Expiration == nil {
		return Token{}, errors.New("STS returned no token expiry")
	}
	return Token{Value: aws.ToString(result.WebIdentityToken), ExpiresAt: *result.Expiration}, nil
}

func RunCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("sts-token-refresher", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	var audience, connectionName string
	var interval time.Duration
	var region string
	flags.StringVar(&audience, "audience", "", "Temporal STS token audience")
	flags.StringVar(&connectionName, "connection-name", "", "ClusterConnection name to sync")
	flags.DurationVar(&interval, "sync-interval", 30*time.Second, "WorkerDeployment namespace refresh interval")
	flags.StringVar(&region, "region", os.Getenv("AWS_REGION"), "AWS region for STS")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: temporal-worker-controller sts-token-refresher [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if audience == "" || connectionName == "" {
		return errors.New("--audience and --connection-name are required")
	}
	configOptions := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		configOptions = append(configOptions, awsconfig.WithRegion(region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, configOptions...)
	if err != nil {
		return fmt.Errorf("load AWS credentials: %w", err)
	}
	kubeCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("load in-cluster Kubernetes config: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeCfg)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(kubeCfg)
	if err != nil {
		return fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}
	refresher := &Refresher{
		Audience:       audience,
		ConnectionName: connectionName,
		Interval:       interval,
		Issuer:         stsTokenIssuer{client: sts.NewFromConfig(awsCfg), audience: audience},
		Kubernetes:     kubeClient,
		Dynamic:        dynamicClient,
		Logf: func(format string, args ...any) {
			slog.Info(fmt.Sprintf(format, args...))
		},
	}
	return runAsLeader(ctx, kubeClient, os.Getenv("POD_NAMESPACE"), os.Getenv("POD_NAME"), refresher)
}

func runAsLeader(ctx context.Context, kubeClient kubernetes.Interface, namespace, identity string, refresher *Refresher) error {
	if namespace == "" || identity == "" {
		return errors.New("POD_NAMESPACE and POD_NAME must be set")
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      tokenRefresherLeaseName(refresher.ConnectionName),
			Namespace: namespace,
		},
		Client: kubeClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}
	logger := slog.Default()
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Name:            tokenRefresherLeaseName(refresher.ConnectionName),
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				if err := refresher.Run(leaderCtx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error("token refresher stopped", "error", err)
				}
			},
			OnStoppedLeading: func() {
				logger.Info("token refresher stopped leading")
			},
			OnNewLeader: func(newLeader string) {
				logger.Info("token refresher leader elected", "identity", newLeader)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create token refresher leader election: %w", err)
	}
	elector.Run(ctx)
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func tokenRefresherLeaseName(connectionName string) string {
	const prefix = "temporal-worker-controller-sts-"
	name := prefix + connectionName
	if len(name) <= 253 {
		return name
	}
	hash := sha256.Sum256([]byte(connectionName))
	suffix := "-" + hex.EncodeToString(hash[:8])
	return prefix + connectionName[:253-len(prefix)-len(suffix)] + suffix
}
