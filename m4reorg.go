package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/dhowden/tag"
	"github.com/dwbuiten/go-mediainfo/mediainfo"
	"github.com/h2non/filetype"
	log "github.com/sirupsen/logrus"
)

// own types
type m4ainfo struct {
	artist     string
	album      string
	trackname  string
	comment    string
	trackno    int
	maxtrack   int
	diskno     int
	maxdisk    int
	playlength float32
	filename   string
}
type diskset struct {
	author        string
	book          string
	totalduration int
	splittime     int
	numberofdisks int
	targetparts   int
	picture       string
	chaptermarks  string
	totaltracks   int
	skip          int
	headerinfo    string
	disk          []atrack // the content ordered by disk and pos on disk
	sorted        []m4ainfo
	processlist   [][]int
}
type atrack map[string]m4ainfo
type aalbum map[string]atrack
type aartist map[string]aalbum

var (
	userLogLevel    string
	sourceDirectory string
	audioinfo       m4ainfo
	artistlist      aartist
	// ALLREADYLONGENOUGH : if that long in ms do not process
	ALLREADYLONGENOUGH = 3600000
	// MAXDURATION : max length of target track in seconds
	MAXDURATION = 23400000
	// TMPDIR : for target fiels for ffmpeg
	TMPDIR = "./tmp/"
	// TOOLBINPATH : where to find the external binaries
	TOOLBINPATH = "/usr/local/bin"
	// CHAPTERTITLE : Titel of Chapter in chapter list
	CHAPTERTITLE = "Chapter "
	// TARGETDIR : The paht for processed files
	TARGETDIR = "./target"
)

func init() {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	flag.StringVar(&userLogLevel, "loglevel", "warn", "Loglevel: [error | warn | info | debug | trace]")
	flag.StringVar(&sourceDirectory, "directory", "", "Directory to parse")
	flag.Parse()
	setLogLevel()
	if len(sourceDirectory) == 0 {
		log.Errorln("no directory to work on")
		os.Exit(1)
	}

	if _, err := os.Stat(sourceDirectory); os.IsNotExist(err) {
		log.Errorf("%v is not directory to work on", sourceDirectory)
		os.Exit(2)
	}

	mediainfo.Init()

}
func setLogLevel() {
	switch userLogLevel {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "trace":
		log.SetLevel(log.TraceLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.SetLevel(log.WarnLevel)
	}

}
func checkMaxDiskSetAndAllEqual(author string, book string) bool {
	log.Infof("[Max Disk]: Checking Track Integrity of \"%v: %v\"\n", author, book)
	res := true
	b := artistlist[author][book]
	maxdisk := 0
	//referenceTrackForMaxDisks := nil
	for track := range b {
		tmax := b[track].maxdisk
		if maxdisk == 0 {
			maxdisk = tmax
			//		referenceTrackForMaxDisks = track
		}
		if tmax != maxdisk {
			log.Warnf("[MaxDisk]: author: %s, book %s has inconsistent max disk info\n", author, book)
			res = false
		}
		log.Debugf("[MaxDisk]: author: %s, book: %s, dsk:%v/[-> %v] track: %v/[%v]\n", author, book, b[track].diskno, b[track].maxdisk, b[track].trackno, b[track].maxtrack)
	}
	// if the max disk is still 0, we are done if not check if all disks are present
	if maxdisk == 0 {
		log.Warnf("[MaxDisk]: No Maxdisk in set given for %s:%s", author, book)

	} else {
		// todo add bool return
		res = checkAllDisksInSetPresent(author, book, maxdisk)

	}

	return res
}

func checkAllDisksInSetPresent(author string, book string, maxdisk int) bool {
	r := true
	diskprsnt := make([]bool, maxdisk+1)
	b := artistlist[author][book]
	for track := range b {
		if b[track].diskno != 0 {
			diskprsnt[b[track].diskno] = true
		}
	}
	for j := 1; j <= maxdisk; j++ {
		if diskprsnt[j] == true {
			log.Debugf("%v %v disk  %v is present\n", author, book, j)
		} else {
			log.Errorf("%v %v disk no %v is missing - skipping book\n", author, book, j)
			r = false
		}

	}
	return r
}

func howMuchParts(duration int) int {
	p := int(math.Trunc(float64(duration)/(float64(MAXDURATION)))) + 1
	return p

}

func splitLength(p int, d int) int {
	return int(d / p)

}
func totalPlayTime(a atrack) int {
	d := 0
	//x := len(a)
	for t := range a {
		d += int(a[t].playlength)
	}
	return d
}

func prepareProcessingSet(auth string, book string) *diskset {
	// the payload:
	ds := new(diskset)
	ds.author = auth
	ds.book = book
	ds.totalduration = totalPlayTime(artistlist[auth][book])
	ds.disk = orderedDiskSet(artistlist[auth][book])

	// todo fix this it is quite ugly
	if len(ds.disk) == 0 {
		log.Warnf("Skipping this disk, it is suspicious ...bailing out")
		return nil
	}
	ds.numberofdisks = len(ds.disk)
	ds.targetparts = howMuchParts(ds.totalduration)
	ds.splittime = splitLength(ds.targetparts, ds.totalduration)
	ds.totaltracks = len(artistlist[auth][book])

	// fill the sorted list:
	orderedTracksOnBook(ds)
	splitByParts(ds)
	//fmt.Printf("we have %d elements\n", len(sorted))
	//fmt.Println("What the fuck is going on?", ds.sorted[1].filename)

	// todo
	// ds.picture
	// ds.chaptermarks
	return ds

}
func prepareTmpDir() error {
	tmpdi := TMPDIR
	err := os.MkdirAll(tmpdi, 0700)
	if err != nil {
		log.Errorf("cannot use %v", tmpdi)
		os.Exit(3)
		// todo add error handler
	}
	// wipe it out
	d, err := os.Open(tmpdi)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(tmpdi, name))
		if err != nil {
			return err
		}
	}
	return nil

}
func fcopy(src string, dst string) {
	// Read all content of src to data
	data, err := ioutil.ReadFile(src)
	if err != nil {
		panic(extractImageExternal)
	}
	// Write data to dst
	err = ioutil.WriteFile(dst, data, 0644)
	if err != nil {
		panic(extractImageExternal)
	}
}
func extractImageExternal(f string) {
	// copy one file
	t := TMPDIR + "/tmpaudio.m4a"
	//io.Copy(f, t)

	fcopy(f, t)
	cmd := TOOLBINPATH + "/mp4art"
	args := []string{"--extract", "--art-index", "0", t}
	if err := exec.Command(cmd, args...).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log.Debugf("Successfully extracted cover image")

}
func openFiles(p int, prefix string) []*os.File {
	ts := make([]*os.File, p)
	for f := range ts {
		fo, err := os.Create(TMPDIR + "/" + prefix + strconv.Itoa(f+1) + ".txt")
		if err != nil {
			panic(err)
		}
		ts[f] = fo
	}

	// close fo on exit and check for its returned error

	return ts
}

func generateHeader(book *diskset, fs []*os.File) {
	h := ";FFMETADATA1\nmajor_brand=M4A\nminor_version=0\ncompatible_brands=M4A mp42isom\n"
	h = h + "comment=" + book.sorted[0].comment + "\n"
	//      h = h + "title=" + book.book + "\n"
	h = h + "comment=" + book.sorted[0].comment + "\n"
	//      h = h + "title=" + book.book + "\n"
	h = h + "artist=" + book.author + "\n"
	h = h + "mediatype=2\n"
	h = h + "album=" + book.book + "\n"
	h = h + "Encoding Params=vers\n"
	for f := range fs {
		_, err := fs[f].WriteString(h)
		if err != nil {
			panic(err)
		}
		_, err = fs[f].WriteString("title=" + book.book + " Teil " + strconv.Itoa(f+1) + "\n")
		if err != nil {
			panic(err)
		}
	}

	/* The header should look like this.
	;FFMETADATA1
	major_brand=M4A
	minor_version=0
	compatible_brands=M4A mp42isom
	comment=Paula (A comment)
	title=6a - A title
	artist=Doe, John
	album=The book title
	date=2012
	media_type=2
	genre=Hörbuch
	Encoding Params=vers
	encoder=Lavf57.71.100
	[CHAPTER]
	TIMEBASE=1/1000
	START=0
	END=445000
	title=Kapitel 1
	[CHAPTER]
	TIMEBASE=1/1000
	START=445000
	END=684000
	title=Kapitel 2

	*/
}
func splitByParts(book *diskset) {
	prepareTmpDir()
	ts := openFiles(book.targetparts, "ffmpegfilelist_part_")
	tm := openFiles(book.targetparts, "ffmpegmetainfo_part_")

	generateHeader(book, tm)
	extractImageExternal(book.sorted[0].filename)
	//tracktime := 0
	lasttime := 0
	marktime := 0
	part := 1

	// write header to files
	for t := 0; t < book.totaltracks; t++ {
		lasttime = marktime
		marktime = marktime + int(book.sorted[t].playlength)
		//      marktime = tracktime
		if marktime > book.splittime && part < book.targetparts {
			// todo check it that works, it should but...
			log.Infof("%s, %s new part %d on %d splittime: %d, maxprt: %d", book.author, book.book, part+1, marktime, book.splittime, book.targetparts)
			//tracktime = 0
			marktime = int(book.sorted[t].playlength)
			lasttime = 0
			part++

		}
		// todo ein Kapitel fehlt!

		_, err := tm[part-1].WriteString("[CHAPTER]\nTIMEBASE=1/1000\nSTART=" + strconv.Itoa(lasttime) + "\nEND=" + strconv.Itoa(marktime) + "\ntitle=" + CHAPTERTITLE + strconv.Itoa(t+1) + "\n")

		if err != nil {
			panic(err)
		}
		//_, err := ts[part-1].String(fmt.Sprintf("%s %s part %d %d  %s\n", book.author, book.book, part, t, book.sorted[t].filename))
		//_, err = ts[part-1].WriteString(fmt.Sprintf("file '%s'\n", book.sorted[t].filename))
		_, err = ts[part-1].WriteString(fmt.Sprintf("file '%s'\n", strconv.Itoa(t)+".m4a"))
		log.Infof("file %d.m4a on %d from %d to %d track duration %d", t, part, lasttime, marktime, int(book.sorted[t].playlength))
		//_, err := ts[part-1].WriteString("mist\n")
		if err != nil {
			panic(err)
		}
		//log.Warnf("%s %s part %d %d  %s\n", book.author, book.book, part, t, book.sorted[t].filename)
	}
	ts = append(ts, tm...) // join them, just for closing in one batch
	for f := range ts {
		if err := ts[f].Close(); err != nil {
			panic(err)
		}
	}

}

func orderedTracksOnBook(ds *diskset) {
	// fix this is completly broken!
	// something is broken here!
	list := []m4ainfo{}
	for j := 0; j < ds.numberofdisks; j++ {
		t := make([]m4ainfo, len(ds.disk[j]))
		for track := range ds.disk[j] {
			num := ds.disk[j][track].trackno
			item := ds.disk[j][track]
			if num-1 > len(ds.disk[j]) {
				log.Errorf("array violation")
			}
			t[num-1] = item
		}
		list = append(list, t...)
	}
	ds.sorted = list

}

/*
func diskSetInfo(ds *diskset, jsonID string) []string {

	for j := 0; j < ds.numberofdisks; j++ {
		for track := range ds.disk[j] {
			num := ds.disk[j][track].trackname
			logrus.Warnln(num)
		}

	}

	return nil
}
*/
func linkSourceFiles(book *diskset) {

	tmpdi := TMPDIR
	err := os.MkdirAll(tmpdi, 0700)
	if err != nil {
		log.Errorf("cannot use %v as temp dir\n", tmpdi)
		os.Exit(3)
	}
	for f := range book.sorted {
		fp, _ := filepath.Abs(book.sorted[f].filename)
		err := os.Symlink(fp, tmpdi+"/"+strconv.Itoa(f)+".m4a")
		if err != nil {
			log.Errorf("cannot symlink %v to %v\n", book.sorted[f].filename, tmpdi+"/"+strconv.Itoa(f)+".m4a")
			os.Exit(4)
		}
	}
}

func handleError(err error) {
	if err != nil {
		log.Errorf("ERROR: %v\n", err)
		os.Exit(1)
	}
}

func makeTargetDir(ds *diskset) string {

	td := TARGETDIR + "/" + ds.author + "/" + ds.book
	err := os.MkdirAll(td, 0755)
	handleError(err)
	return (td)
}
func processSet() {

	for auth := range artistlist {
		log.Infof("Processing Author %v\n", auth)
		for book := range artistlist[auth] {
			ds := prepareProcessingSet(auth, book)
			//diskSetInfo(ds, "notusedhere")
			if ds == nil {
				log.Warnf("%v : %v is empty\n", auth, book)
				continue
			}
			linkSourceFiles(ds)
			td := makeTargetDir(ds)
			for j := 0; j < ds.targetparts; j++ {
				joinWithFfmpeg(j+1, ds.book, td, ds.targetparts)
			}
			attachImage(ds)
			markAsItunesBook(ds)
			log.Infoln(ds.author + ":" + ds.book + " completed")
		}
	}
}

func markAsItunesBook(book *diskset) {

	cmd := TOOLBINPATH + "/mp4tags"
	for j := 1; j <= book.targetparts; j++ {
		//  "/" + title + "_part_" + strconv.Itoa(p) + ".m4a"
		tf := TARGETDIR + "/" + book.author + "/" + book.book + "/" + book.book + ".m4a"
		if book.targetparts > 1 {
			tf = TARGETDIR + "/" + book.author + "/" + book.book + "/" + book.book + "_part_" + strconv.Itoa(j) + ".m4a"
		}
		args := []string{"-i", "Audiobook", tf}
		if err := exec.Command(cmd, args...).Run(); err != nil {
			log.Errorf("command failed %v %v %v %v\n", os.Stderr, err, cmd, args)
			os.Exit(6)
		}
		log.Debugf("Successfully marked as audiobook")
	}
}

func attachImage(book *diskset) {
	t := TMPDIR + "/tmpaudio.art[0].png"
	cmd := TOOLBINPATH + "/mp4art"
	for j := 1; j <= book.targetparts; j++ {
		//  "/" + title + "_part_" + strconv.Itoa(p) + ".m4a"
		tf := TARGETDIR + "/" + book.author + "/" + book.book + "/" + book.book + ".m4a"
		if book.targetparts > 1 {
			tf = TARGETDIR + "/" + book.author + "/" + book.book + "/" + book.book + "_part_" + strconv.Itoa(j) + ".m4a"
		}
		args := []string{"--add", t, tf}
		if err := exec.Command(cmd, args...).Run(); err != nil {
			log.Errorf("image attaching failed: %v %v %v %v\n", os.Stderr, err, cmd, args)
			os.Exit(5)
		}
		log.Debugf("Successfully attached cover image")
	}
}
func joinWithFfmpeg(p int, title string, td string, total int) {

	cmd := TOOLBINPATH + "/ffmpeg"
	pa := TMPDIR + "/"
	prt := ""
	if total > 1 {
		prt = "_part_" + strconv.Itoa(p)
	}
	// insert improvement here:
	args := []string{"-f", "concat", "-y", "-safe", "1", "-i", pa + "ffmpegfilelist_part_" + strconv.Itoa(p) + ".txt", "-i",
		pa + "ffmpegmetainfo_part_" + strconv.Itoa(p) + ".txt", "-map_metadata", "1", "-vn", "-c:a", "copy",
		"-movflags", "faststart", td + "/" + title + prt + ".m4a"}
	log.Infof("starting to join %v%v.m4a\n", title, prt)
	if err := exec.Command(cmd, args...).Run(); err != nil {
		log.Infof("tried: %v %v\n", cmd, args)
		log.Infof("got: %v %v\n", os.Stderr, err)
		os.Exit(5)
	}
	log.Infof("Successfully created target audio file %v\n", td+"/"+title+prt+".m4a")

}

func searchFiles(dir string) {
	// this was a real function before
	log.Debugf("Filenamae, Artist, Album, Title, Track-No, MaxTrack, Disk-No, MaxDisk, Duration\n")
	err := filepath.Walk(dir, doFile)
	if err != nil {
		//fmt.Println(err)
		os.Exit(1)
	}
}

func duration(f string) (float32, error) {

	info, err := mediainfo.Open(f)

	defer info.Close()
	val, err := info.Get("Duration", 0, mediainfo.Audio)
	if err != nil {
		log.Errorln(err)
		return 2, err
	}
	timeint, err := strconv.Atoi(val)

	return float32(timeint), nil

}

func readMetaData(file string) (tag.Metadata, error) {
	f, err := os.Open(file)

	if err != nil {
		log.Errorf("error loading file: %v", err)
		return nil, err
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		log.Errorln(err)
		return nil, err
	}
	return m, nil
}
func fillMetadata(stc *m4ainfo, filename string) {
	dura, err := duration(filename)
	if err != nil {
		fmt.Println(err)
		return
	}
	m, err := readMetaData(filename)

	if err != nil {
		fmt.Println(err)
		return
	}
	log.Infof("filename: %v duration: %v\n", filename, dura)
	//	fmt.Println(m)

	tracknotmp, trackmaxtmp := m.Track()
	disknotmp, maxdisktmp := m.Disc()

	stc.artist = m.Artist()
	stc.album = m.Album()
	stc.trackname = m.Title()
	stc.trackno = tracknotmp
	stc.maxtrack = trackmaxtmp
	stc.diskno = disknotmp
	stc.maxdisk = maxdisktmp
	stc.playlength = dura
	stc.filename = filename
	stc.comment = guessComment(m)
	//	fmt.Println(x)

	//	log.Debugf(" lallfaselcccccciecngtrnkjnkkbjnclkjcrrtncgiufcbdvticv %v| %v| %v|  %v| %v| %v| %v \n", stc.artist, stc.album, stc.trackname, stc.trackno, stc.maxtrack, stc.diskno, stc.maxdisk)
}

func guessComment(m tag.Metadata) string {
	/* this is trial and error on comment tags in m4a */
	t, ok := m.Raw()["\xa9cmt"]
	if ok == true {
		return t.(string)
	}
	return ""
}
func checkType(filename string) bool {

	// golang detects m4a audio as video/mp4 no idea why
	r := false
	fi, _ := os.Stat(filename)

	if fi.Mode().IsDir() == true {
		return false
	}
	buf, _ := ioutil.ReadFile(filename)

	kind, unkwown := filetype.Match(buf)
	if unkwown != nil {
		//fmt.Printf("Unkwown: %s", unkwown)
		r = false
	}
	switch kind.Extension {
	case "mp4":
		r = true
	case "m4a":
		r = true
	default:
		r = false
	}

	//fmt.Printf("File type: %s. MIME: %s\n", kind.Extension, kind.MIME.Value)
	return r
}

func insertDataToMap(data m4ainfo, filename string) {
	if len(artistlist) == 0 {
		artistlist = make(aartist)

	}
	if len(artistlist[data.artist]) == 0 {
		artistlist[data.artist] = make(aalbum)

	}
	if len(artistlist[data.artist][data.album]) == 0 {
		artistlist[data.artist][data.album] = make(atrack)
	}
	artistlist[data.artist][data.album][filename] = data

}

func doFile(path string, f os.FileInfo, err error) error {
	r := checkType(path)
	if r != true {
		return nil
	}

	fillMetadata(&audioinfo, path)

	//fullmap[path] = audioinfo
	insertDataToMap(audioinfo, path)

	// fmt.Printf("Visited: %s\n", path)
	return err
}

func areThereAnyPartsToJoin(auth string, book string) bool {
	r := true
	b := artistlist[auth][book]
	if len(b) < 2 {
		log.Warnf("[nothing to do:] %v %v has just one file - nothing to join\n", auth, book)
		r = false
	}
	return r
}
func getSomeKey(m atrack) string {
	for k := range m {
		return k
	}
	return ""
}
func allreadyLongEnough(auth string, book string) bool {
	r := true
	b := artistlist[auth][book]
	x := getSomeKey(b)

	if int(b[x].playlength) > ALLREADYLONGENOUGH {
		log.Warnf("[nothing to do]: %s %s has already long parts\n", auth, book)
		r = false
	}
	return r
}

func orderedDiskSet(trackset atrack) []atrack {
	// how many disks?
	// remember the slice starts with 0, disk no starting with 1
	anytrack := getSomeKey(trackset)
	maxdisk := trackset[anytrack].maxdisk
	if maxdisk == 0 {
		log.Warnf("[MaxTrack] No diskset information for this book")
		return nil
	}
	ts := make([]atrack, maxdisk)
	for j := 0; j < maxdisk; j++ {
		ts[j] = make(atrack)
	}
	for track := range trackset {
		ts[trackset[track].diskno-1][track] = trackset[track]
	}
	return ts
}

func checkIfAllTracksInOrderArePresent(disk atrack) bool {
	r := true
	trackprsnt := make([]bool, len(disk)+1)
	tracksInSet := len(disk)
	/// maxtrack := 0
	for t := range disk {
		// check if maxtrack as metainfo is set at all, if not this is is useless
		if disk[t].maxtrack == 0 {
			log.Warningf("[Track] %v,%v,disk %v has no maxtrack info where %v are on disk", disk[t].artist, disk[t].album, disk[t].diskno, tracksInSet)
			break
		}

		if disk[t].maxtrack != tracksInSet {
			log.Warningf("[Track] %v,%v,disk %v has maxtrack %v where %v are on disk", disk[t].artist, disk[t].album, disk[t].diskno, disk[t].maxtrack, tracksInSet)
		}
		// check if track number is set at all
		if disk[t].trackno == 0 {
			log.Warningf("[Track] %v,%v,disk %v, filename %s has no track number set", disk[t].artist, disk[t].album, disk[t].diskno, disk[t].filename)
			// todo at some code to guess the tracknumber from filename
			r = false
			break
		}
		// was this number already used?
		if disk[t].trackno <= len(disk) && trackprsnt[disk[t].trackno] == true {
			log.Warningf("[Track] %v,%v,disk %v, filename %s has %d which is already used", disk[t].artist, disk[t].album, disk[t].diskno, disk[t].filename, disk[t].trackno)
			r = false
			break
		}
		// finally
		if disk[t].trackno <= len(disk) {
			trackprsnt[disk[t].trackno] = true
		}
	}
	// all in place?
	anykey := getSomeKey(disk)
	for j := 1; j <= len(disk); j++ {
		if trackprsnt[j] == false {

			log.Errorf("[Track]: Number: %v on %v,%v,disk %v is missing\n", j, disk[anykey].artist, disk[anykey].album, disk[anykey].diskno)
			r = false
		}
	}
	log.Infof("Author: %v, Book: %v, Disk No. %v is complete and in order\n", disk[anykey].artist, disk[anykey].album, disk[anykey].diskno)
	return r
}

func checkMaxTrackAndAllPresent(author string, book string) bool {
	r := true
	b := artistlist[author][book]
	tracksByDisk := orderedDiskSet(b)
	//r := true
	//fmt.Println(len(tracksByDisk))
	for disk := range tracksByDisk {
		r = checkIfAllTracksInOrderArePresent(tracksByDisk[disk])
		if r != true {
			break
		}

	}
	//log.Warnf("no of disk in set %d", len(ts))
	return r
}
func checkIntegrity() {
	for auth := range artistlist {
		//      fmt.Printf("author: [%s] \n", auth)
		for book := range artistlist[auth] {
			//      is this already "done"?
			t := areThereAnyPartsToJoin(auth, book)
			if t != true {
				delete(artistlist[auth], book)

				continue
			}
			t = allreadyLongEnough(auth, book)
			if t != true {
				delete(artistlist[auth], book)
				//break
				continue
			}

			t = checkMaxDiskSetAndAllEqual(auth, book)
			if t != true {
				delete(artistlist[auth], book)
				//break
				continue
			}

			t = checkMaxTrackAndAllPresent(auth, book)
			if t != true {
				delete(artistlist[auth], book)
				//break
				continue
			}
		}
	}

}

func main() {
	log.Debug("logging started")
	searchFiles(sourceDirectory)
	checkIntegrity()
	processSet()

}
