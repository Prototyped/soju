package database

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

const schema = `
CREATE TABLE User (
	id INTEGER PRIMARY KEY,
	username VARCHAR(255) NOT NULL UNIQUE,
	password VARCHAR(255),
	admin INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE Network (
	id INTEGER PRIMARY KEY,
	name VARCHAR(255),
	user INTEGER NOT NULL,
	addr VARCHAR(255) NOT NULL,
	nick VARCHAR(255) NOT NULL,
	username VARCHAR(255),
	realname VARCHAR(255),
	pass VARCHAR(255),
	connect_commands VARCHAR(1023),
	sasl_mechanism VARCHAR(255),
	sasl_plain_username VARCHAR(255),
	sasl_plain_password VARCHAR(255),
	sasl_external_cert BLOB DEFAULT NULL,
	sasl_external_key BLOB DEFAULT NULL,
	FOREIGN KEY(user) REFERENCES User(id),
	UNIQUE(user, addr, nick),
	UNIQUE(user, name)
);

CREATE TABLE Channel (
	id INTEGER PRIMARY KEY,
	network INTEGER NOT NULL,
	name VARCHAR(255) NOT NULL,
	key VARCHAR(255),
	detached INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY(network) REFERENCES Network(id),
	UNIQUE(network, name)
);
`

var migrations = []string{
	"", // migration #0 is reserved for schema initialization
	"ALTER TABLE Network ADD COLUMN connect_commands VARCHAR(1023)",
	"ALTER TABLE Channel ADD COLUMN detached INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE Network ADD COLUMN sasl_external_cert BLOB DEFAULT NULL",
	"ALTER TABLE Network ADD COLUMN sasl_external_key BLOB DEFAULT NULL",
	"ALTER TABLE User ADD COLUMN admin INTEGER NOT NULL DEFAULT 0",
	`
		CREATE TABLE UserNew (
			id INTEGER PRIMARY KEY,
			username VARCHAR(255) NOT NULL UNIQUE,
			password VARCHAR(255),
			admin INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO UserNew SELECT rowid, username, password, admin FROM User;
		DROP TABLE User;
		ALTER TABLE UserNew RENAME TO User;
	`,
	`
		CREATE TABLE NetworkNew (
			id INTEGER PRIMARY KEY,
			name VARCHAR(255),
			user INTEGER NOT NULL,
			addr VARCHAR(255) NOT NULL,
			nick VARCHAR(255) NOT NULL,
			username VARCHAR(255),
			realname VARCHAR(255),
			pass VARCHAR(255),
			connect_commands VARCHAR(1023),
			sasl_mechanism VARCHAR(255),
			sasl_plain_username VARCHAR(255),
			sasl_plain_password VARCHAR(255),
			sasl_external_cert BLOB DEFAULT NULL,
			sasl_external_key BLOB DEFAULT NULL,
			FOREIGN KEY(user) REFERENCES User(id),
			UNIQUE(user, addr, nick),
			UNIQUE(user, name)
		);
		INSERT INTO NetworkNew
			SELECT Network.id, name, User.id as user, addr, nick,
				Network.username, realname, pass, connect_commands,
				sasl_mechanism, sasl_plain_username, sasl_plain_password,
				sasl_external_cert, sasl_external_key
			FROM Network
			JOIN User ON Network.user = User.username;
		DROP TABLE Network;
		ALTER TABLE NetworkNew RENAME TO Network;
	`,
}

type sqlite3DB struct {
	lock sync.RWMutex
	db   *sql.DB
}

func openSQLite3(source string) (DB, error) {
	sqlDB, err := sql.Open("sqlite3", source)
	if err != nil {
		return nil, err
	}

	db := &sqlite3DB{db: sqlDB}
	if err := db.upgrade(); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *sqlite3DB) Close() error {
	db.lock.Lock()
	defer db.lock.Unlock()
	return db.db.Close()
}

func (db *sqlite3DB) upgrade() error {
	db.lock.Lock()
	defer db.lock.Unlock()

	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to query schema version: %v", err)
	}

	if version == len(migrations) {
		return nil
	} else if version > len(migrations) {
		return fmt.Errorf("soju (version %d) older than schema (version %d)", len(migrations), version)
	}

	tx, err := db.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if version == 0 {
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("failed to initialize schema: %v", err)
		}
	} else {
		for i := version; i < len(migrations); i++ {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return fmt.Errorf("failed to execute migration #%v: %v", i, err)
			}
		}
	}

	// For some reason prepared statements don't work here
	_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)))
	if err != nil {
		return fmt.Errorf("failed to bump schema version: %v", err)
	}

	return tx.Commit()
}

func toNullString(s string) sql.NullString {
	return sql.NullString{
		String: s,
		Valid:  s != "",
	}
}

func (db *sqlite3DB) ListUsers() ([]User, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	rows, err := db.db.Query("SELECT id, username, password, admin FROM User")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var password sql.NullString
		if err := rows.Scan(&user.ID, &user.Username, &password, &user.Admin); err != nil {
			return nil, err
		}
		user.Password = password.String
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

func (db *sqlite3DB) GetUser(username string) (*User, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	user := &User{Username: username}

	var password sql.NullString
	row := db.db.QueryRow("SELECT id, password, admin FROM User WHERE username = ?", username)
	if err := row.Scan(&user.ID, &password, &user.Admin); err != nil {
		return nil, err
	}
	user.Password = password.String
	return user, nil
}

func (db *sqlite3DB) StoreUser(user *User) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	password := toNullString(user.Password)

	var err error
	if user.ID != 0 {
		_, err = db.db.Exec("UPDATE User SET password = ?, admin = ? WHERE username = ?",
			password, user.Admin, user.Username)
	} else {
		var res sql.Result
		res, err = db.db.Exec("INSERT INTO User(username, password, admin) VALUES (?, ?, ?)",
			user.Username, password, user.Admin)
		if err != nil {
			return err
		}
		user.ID, err = res.LastInsertId()
	}

	return err
}

func (db *sqlite3DB) DeleteUser(id int64) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	tx, err := db.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM Channel
		WHERE id IN (
			SELECT Channel.id
			FROM Channel
			JOIN Network ON Channel.network = Network.id
			WHERE Network.user = ?
		)`, id)
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM Network WHERE user = ?", id)
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM User WHERE id = ?", id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (db *sqlite3DB) ListNetworks(userID int64) ([]Network, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	rows, err := db.db.Query(`SELECT id, name, addr, nick, username, realname, pass,
			connect_commands, sasl_mechanism, sasl_plain_username, sasl_plain_password,
			sasl_external_cert, sasl_external_key
		FROM Network
		WHERE user = ?`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var networks []Network
	for rows.Next() {
		var net Network
		var name, username, realname, pass, connectCommands sql.NullString
		var saslMechanism, saslPlainUsername, saslPlainPassword sql.NullString
		err := rows.Scan(&net.ID, &name, &net.Addr, &net.Nick, &username, &realname,
			&pass, &connectCommands, &saslMechanism, &saslPlainUsername, &saslPlainPassword,
			&net.SASL.External.CertBlob, &net.SASL.External.PrivKeyBlob)
		if err != nil {
			return nil, err
		}
		net.Name = name.String
		net.Username = username.String
		net.Realname = realname.String
		net.Pass = pass.String
		if connectCommands.Valid {
			net.ConnectCommands = strings.Split(connectCommands.String, "\r\n")
		}
		net.SASL.Mechanism = saslMechanism.String
		net.SASL.Plain.Username = saslPlainUsername.String
		net.SASL.Plain.Password = saslPlainPassword.String
		networks = append(networks, net)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return networks, nil
}

func (db *sqlite3DB) StoreNetwork(userID int64, network *Network) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	netName := toNullString(network.Name)
	netUsername := toNullString(network.Username)
	realname := toNullString(network.Realname)
	pass := toNullString(network.Pass)
	connectCommands := toNullString(strings.Join(network.ConnectCommands, "\r\n"))

	var saslMechanism, saslPlainUsername, saslPlainPassword sql.NullString
	if network.SASL.Mechanism != "" {
		saslMechanism = toNullString(network.SASL.Mechanism)
		switch network.SASL.Mechanism {
		case "PLAIN":
			saslPlainUsername = toNullString(network.SASL.Plain.Username)
			saslPlainPassword = toNullString(network.SASL.Plain.Password)
			network.SASL.External.CertBlob = nil
			network.SASL.External.PrivKeyBlob = nil
		case "EXTERNAL":
			// keep saslPlain* nil
		default:
			return fmt.Errorf("soju: cannot store network: unsupported SASL mechanism %q", network.SASL.Mechanism)
		}
	}

	var err error
	if network.ID != 0 {
		_, err = db.db.Exec(`UPDATE Network
			SET name = ?, addr = ?, nick = ?, username = ?, realname = ?, pass = ?, connect_commands = ?,
				sasl_mechanism = ?, sasl_plain_username = ?, sasl_plain_password = ?,
				sasl_external_cert = ?, sasl_external_key = ?
			WHERE id = ?`,
			netName, network.Addr, network.Nick, netUsername, realname, pass, connectCommands,
			saslMechanism, saslPlainUsername, saslPlainPassword,
			network.SASL.External.CertBlob, network.SASL.External.PrivKeyBlob,
			network.ID)
	} else {
		var res sql.Result
		res, err = db.db.Exec(`INSERT INTO Network(user, name, addr, nick, username,
				realname, pass, connect_commands, sasl_mechanism, sasl_plain_username,
				sasl_plain_password, sasl_external_cert, sasl_external_key)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, netName, network.Addr, network.Nick, netUsername, realname, pass, connectCommands,
			saslMechanism, saslPlainUsername, saslPlainPassword, network.SASL.External.CertBlob,
			network.SASL.External.PrivKeyBlob)
		if err != nil {
			return err
		}
		network.ID, err = res.LastInsertId()
	}
	return err
}

func (db *sqlite3DB) DeleteNetwork(id int64) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	tx, err := db.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec("DELETE FROM Channel WHERE network = ?", id)
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM Network WHERE id = ?", id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (db *sqlite3DB) ListChannels(networkID int64) ([]Channel, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	rows, err := db.db.Query(`SELECT id, name, key, detached
		FROM Channel
		WHERE network = ?`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var ch Channel
		var key sql.NullString
		if err := rows.Scan(&ch.ID, &ch.Name, &key, &ch.Detached); err != nil {
			return nil, err
		}
		ch.Key = key.String
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return channels, nil
}

func (db *sqlite3DB) StoreChannel(networkID int64, ch *Channel) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	key := toNullString(ch.Key)

	var err error
	if ch.ID != 0 {
		_, err = db.db.Exec(`UPDATE Channel
			SET network = ?, name = ?, key = ?, detached = ?
			WHERE id = ?`,
			networkID, ch.Name, key, ch.Detached, ch.ID)
	} else {
		var res sql.Result
		res, err = db.db.Exec(`INSERT INTO Channel(network, name, key, detached)
			VALUES (?, ?, ?, ?)`,
			networkID, ch.Name, key, ch.Detached)
		if err != nil {
			return err
		}
		ch.ID, err = res.LastInsertId()
	}
	return err
}

func (db *sqlite3DB) DeleteChannel(id int64) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	_, err := db.db.Exec("DELETE FROM Channel WHERE id = ?", id)
	return err
}