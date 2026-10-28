package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func workIQNativeMailKey(ctx context.Context, tx *sql.Tx, account, kind string, original, floor uint32) (uint32, error) {
	if original == 0 {
		return 0, nil
	}
	source := strconv.FormatUint(uint64(original), 10)
	var stored sql.NullInt64
	err := tx.QueryRowContext(ctx, `select case when typeof(key)='integer' and key between 1 and 4294967295 then key else 0 end
from workiq_source_keys where account=? and kind=? and namespace='native' and source_id=?`, account, kind, source).Scan(&stored)
	if err == nil {
		if !stored.Valid || stored.Int64 < 1 || stored.Int64 > maxMailSourceKey {
			return 0, errors.New("invalid retained native mail metadata key")
		}
		return uint32(stored.Int64), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var reserved bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_source_keys where account=? and kind=? and key=?)`, account, kind, original).Scan(&reserved); err != nil {
		return 0, err
	}
	if reserved {
		return mailSourceKey(ctx, tx, account, "workiq-native:"+kind, source, floor)
	}
	_, err = tx.ExecContext(ctx, `insert into workiq_source_keys(account,kind,namespace,source_id,key) values(?,?,'native',?,?)`,
		account, kind, source, original)
	return original, err
}

func prepareWorkIQNativeMailMetadata(ctx context.Context, tx *sql.Tx, b MailBatch) (MailBatch, error) {
	var version int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return b, err
	}
	if version < 21 {
		return b, nil
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_source_keys where account=?)`, b.Account).Scan(&active); err != nil {
		return b, err
	}
	if !active {
		return b, nil
	}
	folderSet := map[uint32]bool{b.Result.RootKey: true}
	var folderFloor, attachmentFloor uint32
	for _, folder := range b.Result.Folders {
		folderSet[folder.Key] = true
		folderSet[folder.Parent] = true
	}
	for _, message := range b.Result.Messages {
		folderSet[message.FolderKey] = true
		for _, a := range message.Attachments {
			attachmentFloor = max(attachmentFloor, a.Key)
		}
	}
	for _, coverage := range b.Result.Coverage {
		folderSet[coverage.FolderKey] = true
	}
	var folderKeys []uint32
	for key := range folderSet {
		folderKeys = append(folderKeys, key)
		folderFloor = max(folderFloor, key)
	}
	sort.Slice(folderKeys, func(i, j int) bool { return folderKeys[i] < folderKeys[j] })
	folders := map[uint32]uint32{}
	for _, key := range folderKeys {
		mapped, err := workIQNativeMailKey(ctx, tx, b.Account, "folder", key, folderFloor)
		if err != nil {
			return b, err
		}
		folders[key] = mapped
	}
	b.Result.Folders = append([]outlookmail.Folder(nil), b.Result.Folders...)
	for i, folder := range b.Result.Folders {
		folder.Key, folder.Parent = folders[folder.Key], folders[folder.Parent]
		b.Result.Folders[i] = folder
	}
	b.Result.RootKey = folders[b.Result.RootKey]
	b.Result.Coverage = append([]outlookmail.Coverage(nil), b.Result.Coverage...)
	for i := range b.Result.Coverage {
		b.Result.Coverage[i].FolderKey = folders[b.Result.Coverage[i].FolderKey]
	}
	b.Result.Messages = append([]outlookmail.Message(nil), b.Result.Messages...)
	for i, message := range b.Result.Messages {
		message.FolderKey = folders[message.FolderKey]
		message.Folder.Key = message.FolderKey
		message.Attachments = append([]outlookmail.Attachment(nil), message.Attachments...)
		for j, a := range message.Attachments {
			key, err := workIQNativeMailKey(ctx, tx, b.Account, "attachment", a.Key, attachmentFloor)
			if err != nil {
				return b, err
			}
			a.Key = key
			message.Attachments[j] = a
		}
		b.Result.Messages[i] = message
	}
	return b, nil
}
