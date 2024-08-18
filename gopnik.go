package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/mattn/go-sqlite3"
)

var (
	token              = ""
	remindersChannelId = ""
	dbHandle           *sql.DB
)

func isLeapYear(year int) bool {
	if year%400 == 0 {
		return true
	}

	return year%4 == 0 && year%100 != 0
}

func isAbsoluteInputValid(day int, month int, year int, hour int, minute int, currentYear int) (string, bool) {
	// Validate day and month.
	if day == 0 || day > 31 {
		return fmt.Sprintf("No month has %d days you silly goose.", day), false
	} else if month == 0 {
		return "There is no 0th month my dear pumpkin.", false
	} else if month > 12 {
		return "There aren't that many months!", false
	} else {
		daysInMonths := [12]int{31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

		if isLeapYear(year) {
			daysInMonths[1] = 29
		} else {
			daysInMonths[1] = 28
		}

		if day > daysInMonths[month-1] {
			return fmt.Sprintf("There aren't %d days in this month.", day), false
		}
	}

	// Validate year.
	difference := year - currentYear
	if difference < 0 || difference > 1 {
		return fmt.Sprintf("The year has to be either %d or %d.", currentYear, currentYear+1), false
	}

	// Validate hour.
	if hour == 0 || hour > 12 {
		return "The time has to follow the [12-hour clock](https://en.wikipedia.org/wiki/12-hour_clock).", false
	}

	// Validate minute.
	if minute > 59 {
		return "Are you sure you understand the clock?", false
	}

	return "", true
}

func handleAbsoluteRegexMatch(session *discordgo.Session, message *discordgo.MessageCreate, matches []string) {
	currentTime := time.Now()
	currentYear := currentTime.Year()

	day, _ := strconv.Atoi(matches[1])
	month, _ := strconv.Atoi(matches[2])

	var year int
	if len(matches[3]) > 0 {
		year, _ = strconv.Atoi(matches[3])
	} else {
		year = currentYear
	}

	hour, _ := strconv.Atoi(matches[4])

	var minute int
	if len(matches[5]) > 0 {
		minute, _ = strconv.Atoi(matches[5])
	} else {
		minute = 0
	}

	if errMsg, ok := isAbsoluteInputValid(day, month, year, hour, minute, currentYear); !ok {
		session.ChannelMessageSendReply(message.ChannelID, errMsg, message.Reference())

		return
	}

	period, toRemind := matches[6], matches[7]

	// Hour in the 12-hour format is needed later for the information for the user,
	// but the database expects the 24-hour format.
	dbHour := hour
	if period == "AM" && hour == 12 {
		dbHour = 0
	} else if period == "PM" && hour < 12 {
		dbHour += 12
	}

	targetTime, err := time.ParseInLocation(
		time.DateTime,
		fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d", year, month, day, dbHour, minute, 0),
		currentTime.Location(),
	)
	if err != nil {
		log.Println("Error parsing the time:", err)
		session.ChannelMessageSendReply(
			message.ChannelID,
			"Something went wrong while parsing the time. Check the stderr output.",
			message.Reference(),
		)

		return
	}

	targetTime = targetTime.UTC()
	if targetTime.Before(currentTime) {
		session.ChannelMessageSendReply(
			message.ChannelID,
			"The date cannot be in the past, who would've guessed?",
			message.Reference(),
		)

		return
	}

	_, err = dbHandle.Exec("INSERT INTO reminders VALUES(NULL,?,?,?)", message.Author.ID, targetTime, strings.Replace(toRemind, " my ", " your ", -1))
	if err != nil {
		log.Println("Error inserting into the database:", err)
		session.ChannelMessageSendReply(
			message.ChannelID,
			"Something went wrong while inserting to the DB. Check the stderr output.",
			message.Reference(),
		)

		return
	}

	session.ChannelMessageSendReply(
		message.ChannelID,
		fmt.Sprintf("Successfully added to the database. I'll remind you on %02d.%02d.%d at %02d:%02d %s.", day, month, year, hour, minute, period),
		message.Reference(),
	)
}

func parseRelativeRemindme(matches []string) (int, string, string, time.Time) {
	n, _ := strconv.Atoi(matches[1])
	units, toRemind := matches[2], matches[3]

	targetTime := time.Now().UTC()
	switch units {
	case "minute", "minutes":
		targetTime = targetTime.Add(time.Minute * time.Duration(n))
	case "hour", "hours":
		targetTime = targetTime.Add(time.Hour * time.Duration(n))
	case "day", "days":
		targetTime = targetTime.AddDate(0, 0, n)
	case "week", "weeks":
		targetTime = targetTime.AddDate(0, 0, 7*n)
	case "month", "months":
		targetTime = targetTime.AddDate(0, n, 0)
	default:
		log.Println("Something went really wrong, we shouldn't be here.")
	}

	return n, units, toRemind, targetTime
}

func handleRelativeRegexMatch(session *discordgo.Session, message *discordgo.MessageCreate, matches []string) {
	n, units, toRemind, targetTime := parseRelativeRemindme(matches)
	if n == 0 {
		session.ChannelMessageSendReply(
			message.ChannelID,
			strings.Replace(fmt.Sprintf("Immediately reminding you %s, you silly goose.", toRemind), " my ", " your ", -1),
			message.Reference(),
		)

		return
	}

	_, err := dbHandle.Exec("INSERT INTO reminders VALUES(NULL,?,?,?)", message.Author.ID, targetTime, strings.Replace(toRemind, " my ", " your ", -1))
	if err != nil {
		log.Println("Error inserting into the database:", err)
		session.ChannelMessageSendReply(
			message.ChannelID,
			"Something went wrong while inserting to the DB. Check the stderr output.",
			message.Reference(),
		)

		return
	}

	session.ChannelMessageSendReply(
		message.ChannelID,
		fmt.Sprintf("Successfully added to the database. I'll remind you in %d %s.", n, units),
		message.Reference(),
	)
}

func messageCreate(session *discordgo.Session, message *discordgo.MessageCreate) {
	// Ignore the bot's own messages.
	if message.Author.ID == session.State.User.ID {
		return
	}

	// Ignore other bots' shenanigans.
	if message.Author.Bot {
		return
	}

	const absoluteRemindmeRegex = `^!remindme on (\d{1,2})\.(\d{1,2})(?:\.(\d{4}))? at (\d{1,2})(?::(\d{1,2}))? (AM|PM) (.+)`
	const relativeRemindmeRegex = `^!remindme in (\d{1,2}) (minutes?|hours?|days?|weeks?|months?) (.+)`

	absoluteRemindmeRegexCompiled := regexp.MustCompile(absoluteRemindmeRegex)
	relativeRemindmeRegexCompiled := regexp.MustCompile(relativeRemindmeRegex)

	doesAbsoluteRegexMatch := absoluteRemindmeRegexCompiled.MatchString(message.Content)
	doesRelativeRegexMatch := relativeRemindmeRegexCompiled.MatchString(message.Content)

	if strings.HasPrefix(message.Content, "!remindme") && !doesAbsoluteRegexMatch && !doesRelativeRegexMatch {
		session.ChannelMessageSendReply(
			message.ChannelID,
			"Invalid `!remindme` syntax. Has to match either of these regexes:\n"+
				fmt.Sprintf("`%s`\n", absoluteRemindmeRegex)+
				fmt.Sprintf("`%s`\n\n", relativeRemindmeRegex)+
				"For example:\n"+
				"`!remindme on 23.12 at 12 PM that Christmas is tomorrow`\n"+
				"`!remindme in 2 days to buy a gift for Aurora`",
			message.Reference(),
		)

		return
	}

	if !doesAbsoluteRegexMatch && !doesRelativeRegexMatch {
		return
	}

	if doesAbsoluteRegexMatch {
		handleAbsoluteRegexMatch(session, message, absoluteRemindmeRegexCompiled.FindStringSubmatch(message.Content))
		return
	}

	handleRelativeRegexMatch(session, message, relativeRemindmeRegexCompiled.FindStringSubmatch(message.Content))
}

func handleReminders(botSession *discordgo.Session, ticker *time.Ticker) {
	for currentTime := range ticker.C {
		if _, err := os.Stat("./reminders.db"); errors.Is(err, os.ErrNotExist) {
			log.Println("Database not bootstrapped yet, nothing to check.")
			continue
		}

		rows, err := dbHandle.Query("SELECT * FROM reminders")
		if err != nil {
			log.Println("Error querying the rows when handling the reminders:", err)
		}

		rowsToDelete := []string{}
		for rows.Next() {
			var (
				id       string
				who      string
				time     time.Time
				toRemind string
			)

			if err := rows.Scan(&id, &who, &time, &toRemind); err != nil {
				log.Println("Error scanning the row:", err)
			}

			if currentTime.UTC().After(time) {
				botSession.ChannelMessageSend(remindersChannelId, fmt.Sprintf("<@%s>, reminding you %s.", who, toRemind))
				rowsToDelete = append(rowsToDelete, id)
			}
		}
		rows.Close()

		placeholders := make([]string, len(rowsToDelete))
		for i := range placeholders {
			placeholders[i] = "?"
		}

		_, err = dbHandle.Exec(
			fmt.Sprintf("DELETE FROM reminders WHERE id IN (%s)", strings.Join(rowsToDelete, ",")),
		)
		if err != nil {
			log.Println("Error deleting the rows:", err)
		}
	}
}

func bootstrapDb() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "reminders.db")
	if err != nil {
		return db, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS reminders (
			id INTEGER NOT NULL PRIMARY KEY,
			who TEXT NOT NULL,
			time DATETIME NOT NULL,
			toRemind TEXT NOT NULL
		);`)
	if err != nil {
		return db, err
	}

	return db, nil
}

func init() {
	token = os.Getenv("GOPNIK_TOKEN")
	if len(token) == 0 {
		log.Fatalln("Bot token not found. Make sure to set the GOPNIK_TOKEN environment variable.")
	}

	remindersChannelId = os.Getenv("REMINDERS_CHANNEL")
	if len(remindersChannelId) == 0 {
		log.Fatalln("Reminders channel ID not found. Make sure to set the REMINDERS_CHANNEL environment variable.")
	}

	var err error
	dbHandle, err = bootstrapDb()
	if err != nil {
		log.Fatalln("Error bootstrapping the database:", err)
	}
}

func main() {
	defer dbHandle.Close()

	botSession, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalln("Error creating the bot session:", err)
	}

	botSession.AddHandler(messageCreate)

	botSession.Identify.Intents = discordgo.IntentsGuildMessages

	err = botSession.Open()
	if err != nil {
		log.Fatalln("Error opening the WebSocket connection:", err)
	}
	defer botSession.Close()

	ticker := time.NewTicker(time.Minute)
	go handleReminders(botSession, ticker)

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	fmt.Println("Shutting down...")
	ticker.Stop()
}
