package app

import (
	"errors"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

func mysqlErrorNumber(err error) uint16 {
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number
	}
	return 0
}

func isDuplicateKey(err error) bool { return mysqlErrorNumber(err) == 1062 }

func isForeignKeyViolation(err error) bool {
	number := mysqlErrorNumber(err)
	return number == 1451 || number == 1452
}
