package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gojenkins "github.com/jenkins-x/golang-jenkins"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jenkins-x/jx/pkg/jx/cmd/opts"
	"github.com/jenkins-x/jx/pkg/jx/cmd/templates"
	"github.com/jenkins-x/jx/pkg/log"
)

// GetOptions is the start of the data required to perform the operation.  As new fields are added, add them here instead of
// referencing the cmd.Flags()
type GCActivitiesOptions struct {
	*opts.CommonOptions

	RevisionHistoryLimit int
	PullRequestHours     int
	jclient              gojenkins.JenkinsClient
}

var (
	GCActivitiesLong = templates.LongDesc(`
		Garbage collect the Jenkins X Activity Custom Resource Definitions

`)

	GCActivitiesExample = templates.Examples(`
		jx garbage collect activities
		jx gc activities
`)
)

// NewCmd s a command object for the "step" command
func NewCmdGCActivities(commonOpts *opts.CommonOptions) *cobra.Command {
	options := &GCActivitiesOptions{
		CommonOptions: commonOpts,
	}

	cmd := &cobra.Command{
		Use:     "activities",
		Short:   "garbage collection for activities",
		Long:    GCActivitiesLong,
		Example: GCActivitiesExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Cmd = cmd
			options.Args = args
			err := options.Run()
			CheckErr(err)
		},
	}
	cmd.Flags().IntVarP(&options.RevisionHistoryLimit, "revision-history-limit", "l", 5, "Minimum number of Activities per application to keep")
	cmd.Flags().IntVarP(&options.PullRequestHours, "pull-request-hours", "p", 48, "Number of hours to keep pull request activities for")
	return cmd
}

// Run implements this command
func (o *GCActivitiesOptions) Run() error {
	client, currentNs, err := o.JXClientAndDevNamespace()
	if err != nil {
		return err
	}

	// cannot use field selectors like `spec.kind=Preview` on CRDs so list all environments
	activities, err := client.JenkinsV1().PipelineActivities(currentNs).List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(activities.Items) == 0 {
		// no preview environments found so lets return gracefully
		if o.Verbose {
			log.Info("no activities found\n")
		}
		return nil
	}

	prowEnabled, err := o.IsProw()
	if err != nil {
		return err
	}

	var jobNames []string
	if !prowEnabled {
		o.jclient, err = o.JenkinsClient()
		if err != nil {
			return err
		}

		jobs, err := o.jclient.GetJobs()
		if err != nil {
			return err
		}
		for _, j := range jobs {
			err = o.GetAllPipelineJobNames(o.jclient, &jobNames, j.Name)
			if err != nil {
				return err
			}
		}
	}

	activityBuilds := make(map[string][]int)

	for _, a := range activities.Items {
		// if the activity is a PR and has completed over a week ago lets GC it
		if strings.Contains(a.Name, "-pr-") {
			if a.Spec.CompletedTimestamp != nil && a.Spec.CompletedTimestamp.Add(time.Duration(o.PullRequestHours)*time.Hour).Before(time.Now()) {
				err = client.JenkinsV1().PipelineActivities(currentNs).Delete(a.Name, metav1.NewDeleteOptions(0))
				if err != nil {
					return err
				}
				continue
			}
		}

		if !prowEnabled {
			// if activity has no job in Jenkins delete it
			matched := false
			for _, j := range jobNames {
				if a.Spec.Pipeline == j {
					matched = true
					break
				}
			}
			if !matched {
				err = client.JenkinsV1().PipelineActivities(currentNs).Delete(a.Name, metav1.NewDeleteOptions(0))
				if err != nil {
					return err
				}
			}
		}

		buildNumber, err := strconv.Atoi(a.Spec.Build)
		if err != nil {
			return err
		}

		// collect all activities for a pipeline
		activityBuilds[a.Spec.Pipeline] = append(activityBuilds[a.Spec.Pipeline], buildNumber)
	}

	for pipeline, builds := range activityBuilds {

		sort.Ints(builds)

		// iterate over the build numbers and delete any while the activity is under the RevisionHistoryLimit
		i := 0
		for i < len(builds)-o.RevisionHistoryLimit {
			activityName := fmt.Sprintf("%s-%v", pipeline, builds[i])
			activityName = strings.Replace(activityName, "/", "-", -1)
			activityName = strings.Replace(activityName, "_", "-", -1)
			activityName = strings.ToLower(activityName)

			err = client.JenkinsV1().PipelineActivities(currentNs).Delete(activityName, metav1.NewDeleteOptions(0))
			if err != nil {
				return fmt.Errorf("failed to delete activity %s: %v\n", activityName, err)
			}

			i++
		}
	}

	return nil
}
