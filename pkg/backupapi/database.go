package backupapi

import (
	"context"
	"os"

	pg "github.com/habx/pg-commands"
	"go.uber.org/zap"
)

const (
	dump_path = "/tmp/bizfly-backup/postgres"
)

type Database struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
}

func (c *Client) BackupPostgres(ctx context.Context) (error, string) {

	dump, _ := pg.NewDump(&pg.Postgres{
		Host:     c.dataBase.Host,
		Port:     c.dataBase.Port,
		DB:       c.dataBase.Database,
		Username: c.dataBase.Username,
		Password: c.dataBase.Password,
	})
	err := os.MkdirAll(dump_path, 0700)
	if err != nil {
		c.logger.Error("err", zap.Error(err))
		return err, ""
	}
	dump.SetPath(dump_path)
	dumpExec := dump.Exec(pg.ExecOptions{StreamPrint: false})

	if dumpExec.Error != nil {
		c.logger.Error("err", zap.Error(dumpExec.Error.Err))
		c.logger.Error(dumpExec.Output)
		err = dumpExec.Error.Err
	} else {
		c.logger.Info("Dump success")
		c.logger.Info(dumpExec.Output)
		err = nil
	}
	return err, dumpExec.Output
}

func (c *Client) RestorePostgres(ctx context.Context, dumpFile pg.Result) error {
	restore, _ := pg.NewRestore(&pg.Postgres{
		Host:     c.dataBase.Host,
		Port:     c.dataBase.Port,
		DB:       c.dataBase.Database,
		Username: c.dataBase.Username,
		Password: c.dataBase.Password,
	})
	restoreExec := restore.Exec(dumpFile.File, pg.ExecOptions{StreamPrint: false})
	if restoreExec.Error != nil {
		c.logger.Error("err", zap.Error(restoreExec.Error.Err))
		c.logger.Error(restoreExec.Output)

	} else {
		c.logger.Info("Restore success")
		c.logger.Info(restoreExec.Output)
	}
	return nil
}
