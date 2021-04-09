package database

import (
	"github.com/Qovery/pleco/utils"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/rds"
	log "github.com/sirupsen/logrus"
	"strconv"
	"time"
)

type rdsDatabase struct {
	DBInstanceIdentifier string
	InstanceCreateTime time.Time
	DBInstanceStatus string
	TTL int64
}

func RdsSession(sess session.Session, region string) *rds.RDS {
	return rds.New(&sess, &aws.Config{Region: aws.String(region)})
}

func listTaggedRDSDatabases(svc rds.RDS, tagName string) ([]rdsDatabase, error) {
	var taggedDatabases []rdsDatabase

	// unfortunately AWS doesn't support tag filtering for RDS
	result, err := svc.DescribeDBInstances(nil)
	if err != nil {
		return nil, err
	}

	if len(result.DBInstances) == 0 {
		return nil, nil
	}

	for _, instance := range result.DBInstances {
		for _, tag := range instance.TagList {
			if *tag.Key == tagName {
				if *tag.Key == "" {
					log.Warnf("Tag %s was empty and it wasn't expected, skipping", *tag.Key)
					continue
				}

				ttl, err := strconv.Atoi(*tag.Value)
				if err != nil {
					log.Errorf("Error while trying to convert tag value (%s) to integer on instance %s in %s",
						*tag.Value, *instance.DBInstanceIdentifier, *svc.Config.Region)
					continue
				}

				// ignore if creation is in progress to avoid nil fields
				if *instance.DBInstanceStatus == "creating" {
					continue
				}

				taggedDatabases = append(taggedDatabases, rdsDatabase{
					DBInstanceIdentifier: *instance.DBInstanceIdentifier,
					InstanceCreateTime:   *instance.InstanceCreateTime,
					DBInstanceStatus:     *instance.DBInstanceStatus,
					TTL:                  int64(ttl),
				})
			}
		}
	}

	return taggedDatabases, nil
}

func DeleteRDSDatabase(svc rds.RDS, database rdsDatabase) error {
	if database.DBInstanceStatus == "deleting" {
		log.Infof("RDS instance %s is already in deletion process, skipping...", database.DBInstanceIdentifier)
		return nil
	} else {
		log.Infof("Deleting RDS database %s in %s, expired after %d seconds",
			database.DBInstanceIdentifier, *svc.Config.Region, database.TTL)
	}


	_, err := svc.DeleteDBInstance(
		&rds.DeleteDBInstanceInput{
			DBInstanceIdentifier:      aws.String(database.DBInstanceIdentifier),
			DeleteAutomatedBackups:    aws.Bool(true),
			SkipFinalSnapshot:         aws.Bool(true),
		},
	)
	if err != nil {
		return err
	}

	return nil
}

func GetRDSInstanceInfos(svc rds.RDS, databaseIdentifier string) (rdsDatabase, error) {
	input := rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(databaseIdentifier),
	}

	result, err := svc.DescribeDBInstances(&input)
	// ignore if creation is in progress to avoid nil fields
	if err != nil || *result.DBInstances[0].DBInstanceStatus == "creating" {
		return rdsDatabase{
			DBInstanceIdentifier: *result.DBInstances[0].DBInstanceIdentifier,
			InstanceCreateTime:   time.Time{},
			DBInstanceStatus:     *result.DBInstances[0].DBInstanceStatus,
			TTL:                  0,
		}, err
	}

	return rdsDatabase{
		DBInstanceIdentifier: *result.DBInstances[0].DBInstanceIdentifier,
		InstanceCreateTime:   *result.DBInstances[0].InstanceCreateTime,
		DBInstanceStatus:     *result.DBInstances[0].DBInstanceStatus,
		TTL:                  -1,
	}, nil
}

func DeleteExpiredRDSDatabases(svc rds.RDS, tagName string, dryRun bool) {
	databases, err := listTaggedRDSDatabases(svc, tagName)
	region := svc.Config.Region
	if err != nil {
		log.Errorf("can't list RDS databases: %s\n", err)
		return
	}

	var expiredDatabases []rdsDatabase
	for _, database := range databases {
		if utils.CheckIfExpired(database.InstanceCreateTime, database.TTL) {
			expiredDatabases = append(expiredDatabases, database)
		}
	}

	count, start:= utils.ElemToDeleteFormattedInfos("expired RDS database", len(expiredDatabases), *region)

	log.Debug(count)

	if dryRun || len(expiredDatabases) == 0 {
		return
	}

	log.Debug(start)

	for _, database := range expiredDatabases {
		deletionErr := DeleteRDSDatabase(svc, database)
			if deletionErr != nil {
				log.Errorf("Deletion RDS database error %s/%s: %s",
					database.DBInstanceIdentifier, *svc.Config.Region, err)
			}
	}
}