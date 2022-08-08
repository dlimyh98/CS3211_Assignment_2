package main

import "C"

import (
    "context"
    "fmt"
    "io"
    "net"
    "os"
    "time"
)

type outputType uint32
const (
    match outputType = iota
    insert
    cancel
)

type Engine struct{}

type InputStruct struct {
    in input
    timereceived int64
    execid uint32
}

type OutputStruct struct {
    outtype outputType
    in input // of resting order
    activeid uint32
    execid uint32
    timereceived int64
    cancelAccepted bool
}

var donechannel = make(chan struct{})
var IMinputchan = make(chan InputStruct)
var IChannelSlice = make([]chan InputStruct, 0)
var IChannelCancelSlice = make([]chan bool, 0)
var printchan = make(chan OutputStruct, 64)

func (e *Engine) init() {
    go PrintManager()
    go InstrumentManager()
}

func (e *Engine) accept(ctx context.Context, conn net.Conn) {
    go func() {
        <-ctx.Done()
        close(donechannel)
        conn.Close()
    }()
    go handleConn(conn)
}

func handleConn(conn net.Conn) {
    defer conn.Close()

    for {
        in, err := readInput(conn)
        if err != nil {
            if err != io.EOF {
                _, _ = fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
            }
            return
        }
        timeNow := GetCurrentTimestamp()

        IMinputchan <- InputStruct{in, timeNow, 0}
    }
}

func GetCurrentTimestamp() int64 {
    return time.Now().UnixMicro()
}

func InstrumentManager() {
    ICheckSlice := make([]string, 0)
    IChannelSize := 64

    for {
        select {
        case IMinput := <-IMinputchan:
            foundindex := -1

            // check to see if incoming Instrument already exists
            for i := 0; i < len(ICheckSlice); i++ {
                if (ICheckSlice[i] == IMinput.in.instrument) {
                    foundindex = i
                }
            }

            if (IMinput.in.orderType != inputCancel) {
                if (foundindex != -1) {
                    IChannelSlice[foundindex] <- IMinput    // Instrument already exists, add input to the corresponding Instrument's channel
                } else {
                    ICheckSlice = append(ICheckSlice, IMinput.in.instrument)
                    IChannelSlice = append(IChannelSlice, make(chan InputStruct, IChannelSize))    // make space for new Instrument
                    IChannelCancelSlice = append(IChannelCancelSlice, make(chan bool, IChannelSize))
                    appendedIndex := len(ICheckSlice) - 1                                          // get index of appended Instrument
                    IChannelSlice[appendedIndex] <- IMinput
                    go IChannelManager(IChannelSlice[appendedIndex], appendedIndex)                // every Instrument get it's own goRoutine
                }
            } else {
                // Send cancel order to every IChannel
                for i := 0; i < len(ICheckSlice); i++ {
                    IChannelSlice[i] <- IMinput
                }

                // Read from each element in IChannelCancelSlice
                totalfound := false
                for i := 0; i < len(ICheckSlice); i++ {
                    if (<-IChannelCancelSlice[i]) {
                        totalfound = true
                    }
                }

                newO := OutputStruct{cancel, IMinput.in, 0, 0, IMinput.timereceived, totalfound}
                printchan <- newO
            }

        case <-donechannel:
            return
        }
    }
}

func IChannelManager(IChannel chan InputStruct, appendedIndex int) {
    sellSlice := make([]InputStruct, 0)
    buySlice := make([]InputStruct, 0)

    for {
        select {
        case IStruct := <-IChannel:
            if (IStruct.in.orderType == inputSell) {
                // Check through buy slice
                for i := 0; i < len(buySlice); i++ {
                    if (buySlice[i].in.price >= IStruct.in.price && buySlice[i].in.count > 0 && IStruct.in.count > 0) {
                        buySlice[i].execid++;

                        if (IStruct.in.count > buySlice[i].in.count) {
                            // Print order executed
                            newO := OutputStruct{match, buySlice[i].in, IStruct.in.orderId, 
                            buySlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            IStruct.in.count -= buySlice[i].in.count
                            buySlice[i].in.count = 0
                        } else if (IStruct.in.count == buySlice[i].in.count) {
                            // Print order executed
                            newO := OutputStruct{match, buySlice[i].in, IStruct.in.orderId, 
                            buySlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            IStruct.in.count = 0
                            buySlice[i].in.count = 0
                            break
                        } else {
                            // Print order executed
                            newI := buySlice[i].in
                            newI.count = IStruct.in.count
                            newO := OutputStruct{match, newI, IStruct.in.orderId, 
                            buySlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            buySlice[i].in.count -= IStruct.in.count
                            IStruct.in.count = 0
                            break
                        }
                    }
                }

                // Insert into sell slice, which is ascending
                if (IStruct.in.count > 0) {
                    inserted := false

                    for i := 0; i < len(sellSlice); i++ {
                        // find index to insert element
                        if (IStruct.in.price < sellSlice[i].in.price) {
                            inserted = true

                            if (i != 0) {
                                firsthalf := append(sellSlice[:i-1], IStruct)
                                sellSlice = append(firsthalf, sellSlice[i:]...)
                            } else {
                                sellSlice = append([]InputStruct{IStruct}, sellSlice...)
                            }

                            break
                        }
                    }

                    if (inserted == false) {
                        sellSlice = append(sellSlice, IStruct)
                    }

                    newO := OutputStruct{insert, IStruct.in, 0, 0, IStruct.timereceived, false}
                    printchan <- newO
                }

            } else if (IStruct.in.orderType == inputBuy) {
                // Check through sell slice
                for i := 0; i < len(sellSlice); i++ {
                    if (sellSlice[i].in.price <= IStruct.in.price && sellSlice[i].in.count > 0 && IStruct.in.count > 0) {
                        sellSlice[i].execid++;

                        if (IStruct.in.count > sellSlice[i].in.count) {
                            // Print order executed
                            newO := OutputStruct{match, sellSlice[i].in, IStruct.in.orderId, 
                            sellSlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            IStruct.in.count -= sellSlice[i].in.count
                            sellSlice[i].in.count = 0
                        } else if (IStruct.in.count == sellSlice[i].in.count) {
                            // Print order executed
                            newO := OutputStruct{match, sellSlice[i].in, IStruct.in.orderId, 
                            sellSlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            IStruct.in.count = 0
                            sellSlice[i].in.count = 0
                        } else {
                            // Print order executed
                            newI := sellSlice[i].in
                            newI.count = IStruct.in.count
                            newO := OutputStruct{match, newI, IStruct.in.orderId, 
                            sellSlice[i].execid, IStruct.timereceived, false}
                            printchan <- newO

                            sellSlice[i].in.count -= IStruct.in.count
                            IStruct.in.count = 0
                        }
                    }
                }

                // Insert into buy slice, which is descending
                if (IStruct.in.count > 0) {
                    inserted := false
                    for i := 0; i < len(buySlice); i++ {
                        // find index to insert element
                        if (IStruct.in.price > buySlice[i].in.price) {
                            inserted = true

                            if (i != 0) {
                                firsthalf := append(buySlice[:i-1], IStruct)
                                buySlice = append(firsthalf, buySlice[i:]...)
                            } else {
                                buySlice = append([]InputStruct{IStruct}, buySlice...)
                            }

                            break
                        }
                    }

                    if (inserted == false) {
                        buySlice = append(buySlice, IStruct)
                    }

                    newO := OutputStruct{insert, IStruct.in, 0, 0, IStruct.timereceived, false}
                    printchan <- newO
                }

            } else {
                cancelSuccess := false

                // Check through buy slice
                for i := 0; i < len(buySlice); i++ {
                    if (IStruct.in.orderId == buySlice[i].in.orderId && buySlice[i].in.count > 0) {
                        buySlice[i].in.count = 0
                        cancelSuccess = true
                        break
                    }
                }
                // Check through sell slice
                for i := 0; i < len(sellSlice); i++ {
                    if (cancelSuccess) {
                        break
                    }
                    if (IStruct.in.orderId == sellSlice[i].in.orderId && sellSlice[i].in.count > 0) {
                        sellSlice[i].in.count = 0
                        cancelSuccess = true
                        break
                    }
                }

                IChannelCancelSlice[appendedIndex] <- cancelSuccess
            }
        case <-donechannel:
            return
        }
    }
}

func PrintManager() {
    for {
        select {
        case OStruct := <-printchan:
            switch OStruct.outtype {
            case match:
                outputOrderExecuted(OStruct.in.orderId, OStruct.activeid, OStruct.execid, 
                OStruct.in.price, OStruct.in.count, OStruct.timereceived, GetCurrentTimestamp())
            case insert:
                outputOrderAdded(OStruct.in, OStruct.timereceived, GetCurrentTimestamp())
            case cancel:
                outputOrderDeleted(OStruct.in, OStruct.cancelAccepted, OStruct.timereceived, GetCurrentTimestamp())
            }
        case <-donechannel:
            return 
        }
    }
}
