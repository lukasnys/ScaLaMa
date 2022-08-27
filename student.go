package main

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

type Student struct {
	id    string
	name  string
	group int
}

func trimLeftChar(s string) string {
	for i := range s {
		if i > 0 {
			return s[i:]
		}
	}
	return s[:0]
}

// OrgDefinedId, Username, Group
func NewStudent(csvRow []string) *Student {
	s := new(Student)

	s.id = csvRow[0]
	s.name = csvRow[1]

	// Remove # from id
	if s.id[0] == '#' {
		s.id = trimLeftChar(s.id)
	}

	// Remove # from name
	if s.name[0] == '#' {
		s.name = trimLeftChar(s.name)
	}

	// Parse group number: Group # => #
	groupStr := strings.Split(csvRow[2], " ")[1]
	group, err := strconv.Atoi(groupStr)
	if err != nil {
		s.group = -1
	} else {
		s.group = group
	}

	return s
}

func getStudentsFromCsv(file io.Reader) []Student {
	reader := csv.NewReader(file)

	// Getting rid of the header row
	// TODO: throw error if incorrect format
	_, err := reader.Read()
	if err == io.EOF {
		return nil
	}

	var students []Student

	for {
		row, err := reader.Read()

		if err == io.EOF {
			break
		}

		s := NewStudent(row)
		students = append(students, *s)
	}

	return students
}
