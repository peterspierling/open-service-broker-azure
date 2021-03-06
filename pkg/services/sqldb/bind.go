package sqldb

import (
	"fmt"

	"github.com/Azure/open-service-broker-azure/pkg/generate"
	"github.com/Azure/open-service-broker-azure/pkg/service"
	log "github.com/Sirupsen/logrus"
)

func (s *serviceManager) ValidateBindingParameters(
	bindingParameters service.BindingParameters,
) error {
	// There are no parameters for binding to MSSQL, so there is nothing
	// to validate
	return nil
}

func (s *serviceManager) Bind(
	instance service.Instance,
	_ service.BindingParameters,
) (service.BindingDetails, error) {
	dt, ok := instance.Details.(*mssqlInstanceDetails)
	if !ok {
		return nil, fmt.Errorf(
			"error casting instance.Details as *mssqlInstanceDetails",
		)
	}

	loginName := generate.NewIdentifier()
	password := generate.NewPassword()

	// connect to master database to create login
	masterDb, err := getDBConnection(dt, "master")
	if err != nil {
		return nil, err
	}
	defer masterDb.Close() // nolint: errcheck

	if _, err = masterDb.Exec(
		fmt.Sprintf("CREATE LOGIN \"%s\" WITH PASSWORD='%s'", loginName, password),
	); err != nil {
		return nil, fmt.Errorf(
			`error creating login "%s": %s`,
			loginName,
			err,
		)
	}

	// connect to new database to create user for the login
	db, err := getDBConnection(dt, dt.DatabaseName)
	if err != nil {
		return nil, err
	}
	defer db.Close() // nolint: errcheck

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf(
			"error starting transaction on the new database: %s",
			err,
		)
	}
	defer func() {
		if err != nil {
			if err = tx.Rollback(); err != nil {
				log.WithField("error", err).
					Error("error rolling back transaction on the new database")
			}
			// Drop the login created in the last step
			if _, err = masterDb.Exec(
				fmt.Sprintf("DROP LOGIN \"%s\"", loginName),
			); err != nil {
				log.WithField("error", err).
					Error("error dropping login on master database")
			}
		}
	}()
	if _, err = tx.Exec(
		fmt.Sprintf("CREATE USER \"%s\" FOR LOGIN \"%s\"", loginName, loginName),
	); err != nil {
		return nil, fmt.Errorf(
			`error creating user "%s": %s`,
			loginName,
			err,
		)
	}
	if _, err = tx.Exec(
		fmt.Sprintf("GRANT CONTROL to \"%s\"", loginName),
	); err != nil {
		return nil, fmt.Errorf(
			`error granting CONTROL to user "%s": %s`,
			loginName,
			err,
		)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf(
			"error committing transaction on the new database: %s",
			err,
		)
	}

	return &mssqlBindingDetails{
		LoginName: loginName,
		Password:  password,
	}, nil
}

func (s *serviceManager) GetCredentials(
	instance service.Instance,
	binding service.Binding,
) (service.Credentials, error) {
	dt, ok := instance.Details.(*mssqlInstanceDetails)
	if !ok {
		return nil, fmt.Errorf(
			"error casting instance.Details as *mssqlInstanceDetails",
		)
	}
	bd, ok := binding.Details.(*mssqlBindingDetails)
	if !ok {
		return nil, fmt.Errorf(
			"error casting binding.Details as *mssqlBindingDetails",
		)
	}
	return &Credentials{
		Host:     dt.FullyQualifiedDomainName,
		Port:     1433,
		Database: dt.DatabaseName,
		Username: bd.LoginName,
		Password: bd.Password,
	}, nil
}
